#!/usr/bin/env python3
"""Incrementally translate user-facing Markdown docs from docs/ to docs.en/.

Chinese docs in docs/ are the source of truth. This script generates an English
mirror under docs.en/ and stores per-segment translation cache in
`docs.en/.translation-cache.json`.

Only changed Markdown segments are sent to an OpenAI-compatible chat completions
API. Code blocks are copied verbatim.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable

ROOT = Path(__file__).resolve().parents[1]
SOURCE_DIR = ROOT / "docs"
TARGET_DIR = ROOT / "docs.en"
CACHE_PATH = TARGET_DIR / ".translation-cache.json"
AUTO_HEADER_TEMPLATE = "<!-- This file is auto-translated from {source}. Do not edit manually. -->\n\n"

DEFAULT_GLOSSARY = {
    "ElBot": "ElBot",
    "Agent": "Agent",
    "Session": "Session",
    "Chat / Work": "Chat / Work",
    "chat 模式": "chat mode",
    "work 模式": "work mode",
    "工具发现": "tool discovery",
    "常驻记忆": "resident memory",
    "长期记忆": "long-term memory",
    "上下文压缩": "context compaction",
    "平台适配": "platform adapter",
    "输出意图": "output intent",
    "审计日志": "audit log",
    "运行日志": "runtime log",
    "超级管理员": "superadmin",
    "高风险": "high-risk",
    "低风险": "low-risk",
    "命令": "command",
    "会话": "Session",
    "工具": "tool",
    "技能": "Skill",
}

CODE_FENCE_RE = re.compile(r"^\s*(```|~~~)")
HEADING_RE = re.compile(r"^(\s{0,3}#{1,6}\s+)(.*?)(\s+#+\s*)?$")
LIST_RE = re.compile(r"^(\s*(?:[-+*]|\d+[.)])\s+)(.*)$")
QUOTE_RE = re.compile(r"^(\s*>\s?)(.*)$")
HTML_COMMENT_RE = re.compile(r"^\s*<!--.*-->\s*$")
TABLE_SEPARATOR_RE = re.compile(r"^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$")
LINK_RE = re.compile(r"\[[^\]]+\]\(([^)]+)\)")
INLINE_CODE_RE = re.compile(r"`[^`]+`")
SENTENCE_SPLIT_RE = re.compile(r"(?<=[。！？；])")


@dataclass(frozen=True)
class Segment:
    source_file: str
    index: int
    kind: str
    text: str
    context: str

    @property
    def key(self) -> str:
        # Do not include line number or segment index here. If a sentence is
        # inserted near the top of a document, later unchanged segments should
        # still reuse their cached translations.
        digest = sha256_text(self.text)
        return f"{self.source_file}:{self.kind}:{digest}"

    @property
    def hash(self) -> str:
        return sha256_text(self.text)


def sha256_text(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def read_json(path: Path) -> dict:
    if not path.exists():
        return {"version": 1, "segments": {}, "files": {}}
    with path.open("r", encoding="utf-8") as f:
        data = json.load(f)
    if not isinstance(data, dict):
        return {"version": 1, "segments": {}, "files": {}}
    data.setdefault("version", 1)
    data.setdefault("segments", {})
    data.setdefault("files", {})
    return data


def write_json(path: Path, data: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8", newline="\n") as f:
        json.dump(data, f, ensure_ascii=False, indent=2, sort_keys=True)
        f.write("\n")


def source_files() -> list[Path]:
    return sorted(SOURCE_DIR.glob("**/*.md"))


def source_rel(path: Path) -> str:
    return path.relative_to(ROOT).as_posix()


def target_for_source(path: Path) -> Path:
    return TARGET_DIR / path.relative_to(SOURCE_DIR)


def should_translate_text(text: str) -> bool:
    stripped = text.strip()
    if not stripped:
        return False
    if HTML_COMMENT_RE.match(stripped):
        return False
    # Pure commands, paths, separators, numbers, or punctuation do not need LLM.
    if not re.search(r"[\u4e00-\u9fff]", stripped):
        return False
    return True


def split_sentences(text: str) -> list[str]:
    """Split Chinese prose into sentence-sized chunks while preserving whitespace."""
    if "\n" in text:
        # Keep line-oriented Markdown stable. Split the content without the line
        # break, then append the original line break to the last chunk.
        parts: list[str] = []
        lines = text.splitlines(keepends=True)
        for line in lines:
            line_body = line[:-1] if line.endswith("\n") else line
            line_break = "\n" if line.endswith("\n") else ""
            if line_body.strip():
                line_parts = split_sentences(line_body)
                if line_break:
                    line_parts[-1] += line_break
                parts.extend(line_parts)
            else:
                parts.append(line)
        return parts

    if len(text.strip()) <= 60:
        return [text]

    out: list[str] = []
    start = 0
    for match in SENTENCE_SPLIT_RE.finditer(text):
        end = match.end()
        out.append(text[start:end])
        start = end
    if start < len(text):
        out.append(text[start:])
    return [part for part in out if part != ""] or [text]


def split_table_row(line: str) -> list[str]:
    if "|" not in line:
        return [line]
    # str.split preserves empty leading/trailing cells for pipe tables.
    return line.split("|")


def mask_inline_code(text: str) -> tuple[str, dict[str, str]]:
    masks: dict[str, str] = {}

    def repl(match: re.Match[str]) -> str:
        token = f"__ELBOT_CODE_{len(masks)}__"
        masks[token] = match.group(0)
        return token

    return INLINE_CODE_RE.sub(repl, text), masks


def unmask_inline_code(text: str, masks: dict[str, str]) -> str:
    for token, value in masks.items():
        text = text.replace(token, value)
    return text


def collect_heading_context(lines: Iterable[str]) -> str:
    headings = []
    for line in lines:
        match = HEADING_RE.match(line.rstrip("\n"))
        if match:
            headings.append(match.group(2).strip())
    return " > ".join(headings[-3:])


class MarkdownTranslator:
    def __init__(self, cache: dict, client: "TranslationClient", dry_run: bool = False) -> None:
        self.cache = cache
        self.client = client
        self.dry_run = dry_run
        self.segment_index = 0
        self.new_segments: dict[str, dict] = {}
        self.current_headings: list[str] = []

    def translate_file(self, source: Path) -> str:
        rel = source_rel(source)
        lines = source.read_text(encoding="utf-8").splitlines(keepends=True)
        rendered: list[str] = [AUTO_HEADER_TEMPLATE.format(source=rel)]
        in_code = False
        code_fence = ""
        paragraph: list[str] = []

        def flush_paragraph() -> None:
            if not paragraph:
                return
            text = "".join(paragraph)
            rendered.append(self.translate_plain_text(rel, "paragraph", text))
            paragraph.clear()

        for line in lines:
            if CODE_FENCE_RE.match(line):
                flush_paragraph()
                fence = CODE_FENCE_RE.match(line).group(1)  # type: ignore[union-attr]
                if not in_code:
                    in_code = True
                    code_fence = fence
                elif fence == code_fence:
                    in_code = False
                    code_fence = ""
                rendered.append(line)
                continue

            if in_code:
                rendered.append(line)
                continue

            if not line.strip():
                flush_paragraph()
                rendered.append(line)
                continue

            if HTML_COMMENT_RE.match(line):
                flush_paragraph()
                rendered.append(line)
                continue

            stripped = line.rstrip("\n")
            newline = "\n" if line.endswith("\n") else ""

            heading = HEADING_RE.match(stripped)
            if heading:
                flush_paragraph()
                prefix, body, suffix = heading.group(1), heading.group(2), heading.group(3) or ""
                translated = self.translate_plain_text(rel, "heading", body)
                self.current_headings.append(body.strip())
                rendered.append(f"{prefix}{translated.strip()}{suffix}{newline}")
                continue

            if TABLE_SEPARATOR_RE.match(stripped):
                flush_paragraph()
                rendered.append(line)
                continue

            if stripped.lstrip().startswith("|") or ("|" in stripped and stripped.rstrip().endswith("|")):
                flush_paragraph()
                rendered.append(self.translate_table_line(rel, stripped) + newline)
                continue

            list_match = LIST_RE.match(stripped)
            if list_match:
                flush_paragraph()
                prefix, body = list_match.group(1), list_match.group(2)
                rendered.append(prefix + self.translate_plain_text(rel, "list", body).strip() + newline)
                continue

            quote_match = QUOTE_RE.match(stripped)
            if quote_match:
                flush_paragraph()
                prefix, body = quote_match.group(1), quote_match.group(2)
                rendered.append(prefix + self.translate_plain_text(rel, "quote", body).strip() + newline)
                continue

            paragraph.append(line)

        flush_paragraph()
        return "".join(rendered)

    def translate_table_line(self, rel: str, line: str) -> str:
        cells = split_table_row(line)
        out: list[str] = []
        for cell in cells:
            if cell.strip():
                leading = cell[: len(cell) - len(cell.lstrip())]
                trailing = cell[len(cell.rstrip()) :]
                body = cell.strip()
                out.append(leading + self.translate_plain_text(rel, "table", body).strip() + trailing)
            else:
                out.append(cell)
        return "|".join(out)

    def translate_plain_text(self, rel: str, kind: str, text: str) -> str:
        if not should_translate_text(text):
            return text

        pieces = split_sentences(text)
        translated: list[str] = []
        for piece in pieces:
            if not should_translate_text(piece):
                translated.append(piece)
                continue
            translated.append(self.translate_segment(rel, kind, piece))
        return "".join(translated)

    def translate_segment(self, rel: str, kind: str, text: str) -> str:
        self.segment_index += 1
        masked, masks = mask_inline_code(text)
        segment = Segment(
            source_file=rel,
            index=self.segment_index,
            kind=kind,
            text=masked,
            context=" > ".join(self.current_headings[-3:]),
        )
        cached = self.cache.get("segments", {}).get(segment.key)
        if cached and cached.get("source_hash") == segment.hash:
            return unmask_inline_code(cached["translation"], masks)

        if self.dry_run:
            translation = masked
        else:
            translation = self.client.translate(masked, segment.context, rel, kind)
        self.new_segments[segment.key] = {
            "source_hash": segment.hash,
            "source": masked,
            "translation": translation,
            "kind": kind,
            "source_file": rel,
            "updated_at": int(time.time()),
        }
        return unmask_inline_code(translation, masks)


class TranslationClient:
    def __init__(self, api_key: str, base_url: str, model: str, timeout: int = 90, retries: int = 2) -> None:
        if not api_key:
            raise ValueError("TRANSLATE_LLM_API_KEY is required")
        if not base_url:
            raise ValueError("TRANSLATE_LLM_BASE_URL is required")
        if not model:
            raise ValueError("TRANSLATE_LLM_MODEL is required")
        self.api_key = api_key
        self.base_url = base_url.rstrip("/")
        self.model = model
        self.timeout = timeout
        self.retries = retries

    @property
    def endpoint(self) -> str:
        return self.base_url + "/chat/completions"

    def translate(self, text: str, context: str, source_file: str, kind: str) -> str:
        payload = {
            "model": self.model,
            "temperature": 0.2,
            "messages": [
                {"role": "system", "content": self.system_prompt()},
                {
                    "role": "user",
                    "content": json.dumps(
                        {
                            "source_file": source_file,
                            "markdown_kind": kind,
                            "heading_context": context,
                            "text": text,
                        },
                        ensure_ascii=False,
                    ),
                },
            ],
        }
        data = json.dumps(payload).encode("utf-8")
        request = urllib.request.Request(
            self.endpoint,
            data=data,
            headers={
                "Authorization": f"Bearer {self.api_key}",
                "Content-Type": "application/json",
            },
            method="POST",
        )
        last_error: Exception | None = None
        for attempt in range(self.retries + 1):
            try:
                with urllib.request.urlopen(request, timeout=self.timeout) as resp:
                    body = resp.read().decode("utf-8")
                parsed = json.loads(body)
                content = parsed["choices"][0]["message"]["content"]
                return self.clean_translation(str(content), text)
            except (urllib.error.URLError, urllib.error.HTTPError, KeyError, IndexError, json.JSONDecodeError) as exc:
                last_error = exc
                if attempt < self.retries:
                    time.sleep(2**attempt)
                    continue
        raise RuntimeError(f"translation failed for segment: {last_error}")

    @staticmethod
    def system_prompt() -> str:
        glossary = "\n".join(f"- {src} -> {dst}" for src, dst in DEFAULT_GLOSSARY.items())
        return (
            "You translate ElBot user documentation from Simplified Chinese to English.\n"
            "Return only the translated Markdown segment, with no explanations.\n"
            "Rules:\n"
            "- Preserve Markdown syntax.\n"
            "- Preserve inline code placeholders like __ELBOT_CODE_0__ exactly.\n"
            "- Preserve commands, paths, config keys, environment variable names, URLs, and product names.\n"
            "- Do not translate fenced code blocks; they are not sent to you.\n"
            "- Keep links and link targets unchanged unless only the visible Chinese label needs translation.\n"
            "- Keep terminology consistent with this glossary:\n"
            f"{glossary}\n"
        )

    @staticmethod
    def clean_translation(content: str, source: str) -> str:
        content = content.strip("\n")
        # Some models wrap short answers in quotes when the user content is JSON.
        if (content.startswith('"') and content.endswith('"')) or (content.startswith("'") and content.endswith("'")):
            content = content[1:-1]
        if source.endswith("\n"):
            content += "\n"
        return content


def prune_deleted_sources(cache: dict, current_sources: set[str]) -> None:
    cache["files"] = {k: v for k, v in cache.get("files", {}).items() if k in current_sources}
    cache["segments"] = {
        k: v for k, v in cache.get("segments", {}).items() if v.get("source_file") in current_sources
    }
    if TARGET_DIR.exists():
        for target in TARGET_DIR.glob("**/*.md"):
            rel = target.relative_to(TARGET_DIR).as_posix()
            if f"docs/{rel}" not in current_sources:
                target.unlink()


def run(args: argparse.Namespace) -> int:
    cache = read_json(CACHE_PATH)
    sources = source_files()
    current_source_names = {source_rel(path) for path in sources}
    prune_deleted_sources(cache, current_source_names)

    needs_api = False
    for source in sources:
        text = source.read_text(encoding="utf-8")
        if re.search(r"[\u4e00-\u9fff]", text):
            source_hash = sha256_text(text)
            file_entry = cache.get("files", {}).get(source_rel(source), {})
            target = target_for_source(source)
            if file_entry.get("source_hash") != source_hash or not target.exists():
                needs_api = True
                break

    client: TranslationClient
    if args.dry_run:
        client = None  # type: ignore[assignment]
    elif needs_api:
        client = TranslationClient(
            api_key=os.environ.get("TRANSLATE_LLM_API_KEY", ""),
            base_url=os.environ.get("TRANSLATE_LLM_BASE_URL", ""),
            model=os.environ.get("TRANSLATE_LLM_MODEL", ""),
            timeout=args.timeout,
            retries=args.retries,
        )
    else:
        client = None  # type: ignore[assignment]

    staged_outputs: dict[Path, str] = {}
    all_new_segments: dict[str, dict] = {}
    file_entries: dict[str, dict] = {}

    for source in sources:
        rel = source_rel(source)
        translator = MarkdownTranslator(cache=cache, client=client, dry_run=args.dry_run)
        rendered = translator.translate_file(source)
        staged_outputs[target_for_source(source)] = rendered
        all_new_segments.update(translator.new_segments)
        file_entries[rel] = {
            "source_hash": sha256_text(source.read_text(encoding="utf-8")),
            "target": target_for_source(source).relative_to(ROOT).as_posix(),
            "updated_at": int(time.time()),
        }

    # All translations succeeded. Now write outputs atomically enough for CI use.
    TARGET_DIR.mkdir(parents=True, exist_ok=True)
    for target, rendered in staged_outputs.items():
        target.parent.mkdir(parents=True, exist_ok=True)
        with target.open("w", encoding="utf-8", newline="\n") as f:
            f.write(rendered)

    cache.setdefault("segments", {}).update(all_new_segments)
    cache["files"] = file_entries
    write_json(CACHE_PATH, cache)

    if args.dry_run:
        print("dry run completed; source text was copied for changed segments")
    else:
        print(f"translated {len(staged_outputs)} file(s); updated {len(all_new_segments)} segment(s)")
    if needs_api and args.dry_run:
        print("dry run detected source changes that would require API translation")
    return 0


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Incrementally translate docs/ to docs.en/.")
    parser.add_argument("--dry-run", action="store_true", help="Do not call the LLM; copy source text for changed segments.")
    parser.add_argument("--timeout", type=int, default=90, help="HTTP timeout in seconds.")
    parser.add_argument("--retries", type=int, default=2, help="Retry count per segment.")
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    try:
        return run(parse_args(argv))
    except Exception as exc:  # noqa: BLE001 - CLI should print a concise error.
        print(f"translate_docs.py: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
