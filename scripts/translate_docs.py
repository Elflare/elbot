#!/usr/bin/env python3
"""Incrementally translate user-facing Markdown docs from docs/ to docs.en/.

Chinese docs in docs/ are the source of truth. This script generates an English
mirror under docs.en/ and stores per-segment translation cache in
`docs.en/.translation-cache.json`.

Only changed Markdown segments are sent to an OpenAI-compatible chat completions
API. Code blocks are copied verbatim. Changed segments are translated in batches
so the first run does not make one HTTP request per sentence.
"""

from __future__ import annotations

import argparse
import hashlib
import http.client
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SOURCE_DIR = ROOT / "docs"
TARGET_DIR = ROOT / "docs.en"
CACHE_PATH = TARGET_DIR / ".translation-cache.json"
AUTO_HEADER_TEMPLATE = "<!-- This file is auto-translated from {source}. Do not edit manually. -->\n\n"
PLACEHOLDER_TEMPLATE = "\ue000ELBOT_SEGMENT_{index}\ue000"

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
        # Keep keys independent from line number or segment order. If a sentence
        # is inserted near the top, later unchanged segments still reuse cache.
        return f"{self.source_file}:{self.kind}:{sha256_text(self.text)}"

    @property
    def hash(self) -> str:
        return sha256_text(self.text)


@dataclass
class PendingSegment:
    segment: Segment
    masks: dict[str, str]
    placeholder: str


@dataclass
class RenderResult:
    text: str
    pending: list[PendingSegment]
    cache_entries: dict[str, dict]
    reused_count: int
    skipped_count: int


def log(message: str) -> None:
    print(message, flush=True)


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
    return bool(re.search(r"[\u4e00-\u9fff]", stripped))


def split_sentences(text: str) -> list[str]:
    """Split Chinese prose into small chunks while preserving line breaks."""
    if "\n" in text:
        parts: list[str] = []
        for line in text.splitlines(keepends=True):
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

    if len(text.strip()) <= 80:
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


def append_translated_piece(parts: list[str], piece: str) -> None:
    if parts and piece and not parts[-1].endswith((" ", "\n")) and not piece.startswith((" ", "\n")):
        parts.append(" ")
    parts.append(piece)


def chunked(items: list[PendingSegment], size: int) -> list[list[PendingSegment]]:
    return [items[i : i + size] for i in range(0, len(items), size)]


class MarkdownRenderer:
    def __init__(self, cache: dict) -> None:
        self.cache = cache
        self.segment_index = 0
        self.placeholder_index = 0
        self.current_headings: list[str] = []
        self.pending: list[PendingSegment] = []
        self.cache_entries: dict[str, dict] = {}
        self.reused_count = 0
        self.skipped_count = 0

    def render_file(self, source: Path) -> RenderResult:
        rel = source_rel(source)
        lines = source.read_text(encoding="utf-8").splitlines(keepends=True)
        rendered: list[str] = [AUTO_HEADER_TEMPLATE.format(source=rel)]
        in_code = False
        code_fence = ""
        paragraph: list[str] = []

        def flush_paragraph() -> None:
            if not paragraph:
                return
            rendered.append(self.render_plain_text(rel, "paragraph", "".join(paragraph)))
            paragraph.clear()

        for line in lines:
            fence_match = CODE_FENCE_RE.match(line)
            if fence_match:
                flush_paragraph()
                fence = fence_match.group(1)
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
                rendered.append(f"{prefix}{self.render_plain_text(rel, 'heading', body).strip()}{suffix}{newline}")
                self.current_headings.append(body.strip())
                continue

            if TABLE_SEPARATOR_RE.match(stripped):
                flush_paragraph()
                rendered.append(line)
                continue

            if stripped.lstrip().startswith("|") or ("|" in stripped and stripped.rstrip().endswith("|")):
                flush_paragraph()
                rendered.append(self.render_table_line(rel, stripped) + newline)
                continue

            list_match = LIST_RE.match(stripped)
            if list_match:
                flush_paragraph()
                prefix, body = list_match.group(1), list_match.group(2)
                rendered.append(prefix + self.render_plain_text(rel, "list", body).strip() + newline)
                continue

            quote_match = QUOTE_RE.match(stripped)
            if quote_match:
                flush_paragraph()
                prefix, body = quote_match.group(1), quote_match.group(2)
                rendered.append(prefix + self.render_plain_text(rel, "quote", body).strip() + newline)
                continue

            paragraph.append(line)

        flush_paragraph()
        return RenderResult(
            text="".join(rendered),
            pending=self.pending,
            cache_entries=self.cache_entries,
            reused_count=self.reused_count,
            skipped_count=self.skipped_count,
        )

    def render_table_line(self, rel: str, line: str) -> str:
        out: list[str] = []
        for cell in split_table_row(line):
            if cell.strip():
                leading = cell[: len(cell) - len(cell.lstrip())]
                trailing = cell[len(cell.rstrip()) :]
                body = cell.strip()
                out.append(leading + self.render_plain_text(rel, "table", body).strip() + trailing)
            else:
                out.append(cell)
        return "|".join(out)

    def render_plain_text(self, rel: str, kind: str, text: str) -> str:
        if not should_translate_text(text):
            self.skipped_count += 1
            return text

        rendered: list[str] = []
        for piece in split_sentences(text):
            if not should_translate_text(piece):
                rendered.append(piece)
                continue
            append_translated_piece(rendered, self.render_segment(rel, kind, piece))
        return "".join(rendered)

    def render_segment(self, rel: str, kind: str, text: str) -> str:
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
            self.reused_count += 1
            return unmask_inline_code(cached["translation"], masks)

        placeholder = PLACEHOLDER_TEMPLATE.format(index=self.placeholder_index)
        self.placeholder_index += 1
        self.pending.append(PendingSegment(segment=segment, masks=masks, placeholder=placeholder))
        return placeholder


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

    def translate_batch(self, segments: list[PendingSegment]) -> dict[str, str]:
        payload = {
            "model": self.model,
            "temperature": 0.2,
            "messages": [
                {"role": "system", "content": self.system_prompt()},
                {"role": "user", "content": json.dumps(self.batch_payload(segments), ensure_ascii=False)},
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
                return self.parse_batch_response(str(content), segments)
            except (
                urllib.error.URLError,
                urllib.error.HTTPError,
                http.client.HTTPException,
                TimeoutError,
                ConnectionResetError,
                OSError,
                KeyError,
                IndexError,
                json.JSONDecodeError,
                ValueError,
            ) as exc:
                last_error = exc
                if attempt < self.retries:
                    delay = min(2**attempt, 30)
                    log(f"    batch failed, retrying {attempt + 1}/{self.retries} after {delay}s: {exc}")
                    time.sleep(delay)
                    continue
        raise RuntimeError(f"batch translation failed: {last_error}")

    @staticmethod
    def batch_payload(segments: list[PendingSegment]) -> dict:
        return {
            "instruction": "Translate each item from Simplified Chinese to English. Return JSON only.",
            "items": [
                {
                    "key": item.segment.key,
                    "source_file": item.segment.source_file,
                    "markdown_kind": item.segment.kind,
                    "heading_context": item.segment.context,
                    "text": item.segment.text,
                }
                for item in segments
            ],
            "response_format": [{"key": "same key", "translation": "translated text"}],
        }

    @staticmethod
    def system_prompt() -> str:
        glossary = "\n".join(f"- {src} -> {dst}" for src, dst in DEFAULT_GLOSSARY.items())
        return (
            "You translate ElBot user documentation from Simplified Chinese to English.\n"
            "Return only a JSON array. Each item must be {\"key\": string, \"translation\": string}.\n"
            "Rules:\n"
            "- Preserve Markdown syntax inside each segment.\n"
            "- Preserve inline code placeholders like __ELBOT_CODE_0__ exactly.\n"
            "- Preserve commands, paths, config keys, environment variable names, URLs, and product names.\n"
            "- Do not translate fenced code blocks; they are not sent to you.\n"
            "- Keep links and link targets unchanged unless only the visible Chinese label needs translation.\n"
            "- Do not add explanations or extra keys.\n"
            "- Keep terminology consistent with this glossary:\n"
            f"{glossary}\n"
        )

    @staticmethod
    def parse_batch_response(content: str, segments: list[PendingSegment]) -> dict[str, str]:
        content = content.strip()
        if content.startswith("```json"):
            content = content.removeprefix("```json").removesuffix("```").strip()
        elif content.startswith("```"):
            content = content.removeprefix("```").removesuffix("```").strip()
        parsed = json.loads(content)
        if not isinstance(parsed, list):
            raise ValueError("model response is not a JSON array")
        expected = {item.segment.key: item.segment.text for item in segments}
        out: dict[str, str] = {}
        for item in parsed:
            if not isinstance(item, dict):
                raise ValueError("model response item is not an object")
            key = str(item.get("key", ""))
            translation = str(item.get("translation", ""))
            if key not in expected:
                raise ValueError(f"unexpected translation key: {key}")
            source = expected[key]
            translation = translation.strip("\n")
            if source.endswith("\n"):
                translation += "\n"
            out[key] = translation
        missing = set(expected) - set(out)
        if missing:
            raise ValueError(f"missing translation key(s): {', '.join(sorted(missing)[:3])}")
        return out


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


def translate_pending(
    rendered: str,
    pending: list[PendingSegment],
    cache_entries: dict[str, dict],
    client: TranslationClient | None,
    args: argparse.Namespace,
    rel: str,
) -> str:
    if not pending:
        return rendered

    if args.dry_run:
        for item in pending:
            translation = item.segment.text
            rendered = rendered.replace(item.placeholder, unmask_inline_code(translation, item.masks))
            cache_entries[item.segment.key] = cache_entry(item, translation)
        return rendered

    if client is None:
        raise ValueError("translation client is not configured")

    batches = chunked(pending, args.batch_size)
    for index, batch in enumerate(batches, start=1):
        log(f"  translating batch {index}/{len(batches)} ({len(batch)} segment(s))")
        translations = client.translate_batch(batch)
        for item in batch:
            translation = translations[item.segment.key]
            rendered = rendered.replace(item.placeholder, unmask_inline_code(translation, item.masks))
            cache_entries[item.segment.key] = cache_entry(item, translation)
        if args.batch_delay > 0 and index < len(batches):
            log(f"  waiting {args.batch_delay:g}s before next batch")
            time.sleep(args.batch_delay)
    return rendered


def cache_entry(item: PendingSegment, translation: str) -> dict:
    return {
        "source_hash": item.segment.hash,
        "source": item.segment.text,
        "translation": translation,
        "kind": item.segment.kind,
        "source_file": item.segment.source_file,
        "updated_at": int(time.time()),
    }


def make_client(args: argparse.Namespace) -> TranslationClient:
    return TranslationClient(
        api_key=os.environ.get("TRANSLATE_LLM_API_KEY", ""),
        base_url=os.environ.get("TRANSLATE_LLM_BASE_URL", ""),
        model=os.environ.get("TRANSLATE_LLM_MODEL", ""),
        timeout=args.timeout,
        retries=args.retries,
    )


def run(args: argparse.Namespace) -> int:
    cache = read_json(CACHE_PATH)
    sources = source_files()
    current_source_names = {source_rel(path) for path in sources}
    prune_deleted_sources(cache, current_source_names)

    log(f"found {len(sources)} source doc file(s)")
    client: TranslationClient | None = None
    staged_outputs: dict[Path, str] = {}
    all_new_segments: dict[str, dict] = {}
    file_entries: dict[str, dict] = {}
    total_pending = 0
    total_reused = 0

    for source_index, source in enumerate(sources, start=1):
        rel = source_rel(source)
        log(f"[{source_index}/{len(sources)}] rendering {rel}")
        renderer = MarkdownRenderer(cache=cache)
        result = renderer.render_file(source)
        total_pending += len(result.pending)
        total_reused += result.reused_count
        log(
            f"  segments: reused={result.reused_count}, "
            f"changed={len(result.pending)}, skipped={result.skipped_count}"
        )

        if result.pending and not args.dry_run and client is None:
            client = make_client(args)
        rendered = translate_pending(result.text, result.pending, result.cache_entries, client, args, rel)
        staged_outputs[target_for_source(source)] = rendered
        all_new_segments.update(result.cache_entries)
        file_entries[rel] = {
            "source_hash": sha256_text(source.read_text(encoding="utf-8")),
            "target": target_for_source(source).relative_to(ROOT).as_posix(),
            "updated_at": int(time.time()),
        }

    TARGET_DIR.mkdir(parents=True, exist_ok=True)
    for target, rendered in staged_outputs.items():
        target.parent.mkdir(parents=True, exist_ok=True)
        with target.open("w", encoding="utf-8", newline="\n") as f:
            f.write(rendered)

    cache.setdefault("segments", {}).update(all_new_segments)
    cache["files"] = file_entries
    write_json(CACHE_PATH, cache)

    if args.dry_run:
        log("dry run completed; changed segments were copied from source text")
    else:
        log(f"translated {len(staged_outputs)} file(s); updated {len(all_new_segments)} segment(s)")
    log(f"summary: reused={total_reused}, changed={total_pending}, batch_size={args.batch_size}")
    return 0


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Incrementally translate docs/ to docs.en/.")
    parser.add_argument("--dry-run", action="store_true", help="Do not call the LLM; copy source text for changed segments.")
    parser.add_argument("--timeout", type=int, default=240, help="HTTP timeout in seconds.")
    parser.add_argument("--retries", type=int, default=5, help="Retry count per batch.")
    parser.add_argument("--batch-size", type=int, default=8, help="Changed segment count per translation request.")
    parser.add_argument("--batch-delay", type=float, default=5.0, help="Delay between translation batches in seconds.")
    args = parser.parse_args(argv)
    if args.batch_size < 1:
        parser.error("--batch-size must be at least 1")
    if args.batch_delay < 0:
        parser.error("--batch-delay must not be negative")
    return args


def main(argv: list[str]) -> int:
    try:
        return run(parse_args(argv))
    except Exception as exc:  # noqa: BLE001 - CLI should print a concise error.
        print(f"translate_docs.py: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
