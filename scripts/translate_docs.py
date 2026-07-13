#!/usr/bin/env python3
"""Incrementally translate user-facing Markdown docs from docs/ to docs.en/.

Chinese docs in docs/ are the source of truth. This script generates an English
mirror under docs.en/ and stores one per-segment translation cache file per
source document under the repository-local `.translation-cache/` directory.

Only changed Markdown segments are sent to an OpenAI-compatible chat completions
API. Code blocks are copied verbatim except selected documentation comments.
Changed segments are translated in batches so the first run does not make one
HTTP request per sentence.
"""

from __future__ import annotations

import argparse
import hashlib
import http.client
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SOURCE_DIR = ROOT / "docs"
TARGET_DIR = ROOT / "docs.en"
README_SOURCE = ROOT / "README.zh-CN.md"
README_TARGET = ROOT / "README.md"
CHANGELOG_SOURCE = ROOT / "CHANGELOG.md"
CHANGELOG_TARGET = ROOT / "CHANGELOG.en.md"
CACHE_DIR = ROOT / ".translation-cache"
LEGACY_CACHE_PATH = TARGET_DIR / ".translation-cache.json"
AUTO_HEADER_TEMPLATE = "<!-- This file is auto-translated from {source}. Do not edit manually. -->\n\n"
PLACEHOLDER_TEMPLATE = "\ue000ELBOT_SEGMENT_{index}\ue000"

DEFAULT_GLOSSARY = {
    "ElBot": "ElBot",
    "Agent": "Agent",
    "Session": "Session",
    "Chat / Work": "Chat / Work",
    "Elnis": "Elnis",
    "Elwisp": "Elwisp",
    "Elvena": "Elvena",
    "ELyph": "ELyph",
    "ELyph Task Notation": "ELyph Task Notation",
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
    "原生 skill": "native skill",
    "后台 LLM": "background LLM",
    "后台 Session": "background Session",
    "监听枢纽": "listening hub",
    "外部事件": "external event",
    "事件协议": "event protocol",
    "事件模式": "event mode",
    "投递事件": "deliver events",
    "投递目标": "delivery target",
    "安全边界": "security boundary",
    "任务表示法": "Task Notation",
    "艾露妮斯": "Elnis",
    "艾露维娜": "Elvena",
    "艾露维丝": "Elwisp",
}

CODE_FENCE_RE = re.compile(r"^\s*(```|~~~)(.*)$")
HEADING_RE = re.compile(r"^(\s{0,3}#{1,6}\s+)(.*?)(\s+#+\s*)?$")
LIST_RE = re.compile(r"^(\s*(?:[-+*]|\d+[.)])\s+)(.*)$")
QUOTE_RE = re.compile(r"^(\s*>\s?)(.*)$")
HTML_COMMENT_RE = re.compile(r"^\s*<!--.*-->\s*$")
TABLE_SEPARATOR_RE = re.compile(r"^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$")
INLINE_CODE_RE = re.compile(r"`[^`]+`")
SENTENCE_SPLIT_RE = re.compile(r"(?<=[。！？；])")
THINKING_BLOCK_RE = re.compile(r"<\s*(think|thought|thinking)\s*>.*?<\s*/\s*\1\s*>", re.IGNORECASE | re.DOTALL)


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


@dataclass(frozen=True)
class SourceDoc:
    source: Path
    target: Path

    @property
    def source_rel(self) -> str:
        return self.source.relative_to(ROOT).as_posix()

    @property
    def target_rel(self) -> str:
        return self.target.relative_to(ROOT).as_posix()


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


def response_preview(content: str, limit: int = 800) -> str:
    content = content.replace("\r", "\\r").replace("\n", "\\n")
    if len(content) > limit:
        return content[:limit] + "..."
    return content


def normalize_json_response(content: str) -> str:
    content = THINKING_BLOCK_RE.sub("", content).strip()
    if content.startswith("```json"):
        content = content.removeprefix("```json").removesuffix("```").strip()
    elif content.startswith("```"):
        content = content.removeprefix("```").removesuffix("```").strip()
    if content.startswith("["):
        return content
    extracted = extract_first_json_array(content)
    if extracted is not None:
        return extracted
    return content


def extract_first_json_array(content: str) -> str | None:
    start = content.find("[")
    if start < 0:
        return None
    depth = 0
    in_string = False
    escape = False
    for index in range(start, len(content)):
        char = content[index]
        if in_string:
            if escape:
                escape = False
            elif char == "\\":
                escape = True
            elif char == '"':
                in_string = False
            continue
        if char == '"':
            in_string = True
        elif char == "[":
            depth += 1
        elif char == "]":
            depth -= 1
            if depth == 0:
                return content[start : index + 1]
    return None


def read_json(path: Path, default: dict | None = None) -> dict:
    fallback = default or {}
    if not path.exists():
        return dict(fallback)
    try:
        with path.open("r", encoding="utf-8") as f:
            data = json.load(f)
    except (OSError, json.JSONDecodeError) as exc:
        log(f"warning: cannot read cache {path.relative_to(ROOT).as_posix()}: {exc}")
        return dict(fallback)
    if not isinstance(data, dict):
        return dict(fallback)
    return data


def write_json(path: Path, data: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8", newline="\n") as f:
        json.dump(data, f, ensure_ascii=False, indent=2, sort_keys=True)
        f.write("\n")


def cache_path_for_source(rel: str) -> Path:
    source_path = Path(*rel.split("/"))
    return CACHE_DIR / source_path.parent / f"{source_path.name}.json"


def source_for_cache_path(path: Path) -> str:
    rel = path.relative_to(CACHE_DIR).as_posix()
    if not rel.endswith(".json"):
        raise ValueError(f"invalid translation cache path: {rel}")
    return rel.removesuffix(".json")


def empty_doc_cache(doc: SourceDoc) -> dict:
    return {
        "version": 2,
        "source": doc.source_rel,
        "target": doc.target_rel,
        "source_hash": "",
        "updated_at": 0,
        "segments": {},
    }


def read_doc_cache(doc: SourceDoc) -> dict:
    cache = read_json(cache_path_for_source(doc.source_rel), empty_doc_cache(doc))
    if cache.get("version") != 2 or cache.get("source") != doc.source_rel:
        return empty_doc_cache(doc)
    if not isinstance(cache.get("segments"), dict):
        cache["segments"] = {}
    return cache


def migrate_legacy_cache(docs: list[SourceDoc]) -> list[Path]:
    if not LEGACY_CACHE_PATH.exists():
        return []
    legacy = read_json(LEGACY_CACHE_PATH, {"version": 1, "segments": {}, "files": {}})
    files = legacy.get("files")
    segments = legacy.get("segments")
    if not isinstance(files, dict) or not isinstance(segments, dict):
        log("warning: legacy translation cache is invalid; leaving it in place")
        return []

    log("migrating legacy translation cache to per-document files")
    changed_paths: list[Path] = []
    current_sources = {doc.source_rel for doc in docs}
    segments_by_source: dict[str, dict[str, dict]] = {}
    for key, entry in segments.items():
        if not isinstance(entry, dict):
            continue
        source_file = entry.get("source_file")
        if isinstance(source_file, str) and source_file in current_sources:
            segments_by_source.setdefault(source_file, {})[key] = entry

    for doc in docs:
        cache_path = cache_path_for_source(doc.source_rel)
        if cache_path.exists():
            existing = read_json(cache_path)
            if existing.get("version") == 2 and existing.get("source") == doc.source_rel:
                continue
        file_entry = files.get(doc.source_rel, {})
        if not isinstance(file_entry, dict):
            file_entry = {}
        cache = {
            "version": 2,
            "source": doc.source_rel,
            "target": doc.target_rel,
            "source_hash": file_entry.get("source_hash", ""),
            "updated_at": file_entry.get("updated_at", 0),
            "segments": segments_by_source.get(doc.source_rel, {}),
        }
        write_json(cache_path, cache)
        changed_paths.append(cache_path)

    stale_sources = set(files) - current_sources
    for rel in sorted(stale_sources):
        target = target_for_deleted_source(rel)
        if target is not None and target.exists() and target != README_TARGET:
            ensure_generated_path(target)
            target.unlink()
            changed_paths.append(target)

    if all(cache_path_for_source(doc.source_rel).exists() for doc in docs):
        ensure_generated_path(LEGACY_CACHE_PATH)
        LEGACY_CACHE_PATH.unlink()
        changed_paths.append(LEGACY_CACHE_PATH)
    return changed_paths


def all_source_docs() -> list[SourceDoc]:
    docs = [SourceDoc(path, TARGET_DIR / path.relative_to(SOURCE_DIR)) for path in sorted(SOURCE_DIR.glob("**/*.md"))]
    if README_SOURCE.exists():
        docs.insert(0, SourceDoc(README_SOURCE, README_TARGET))
    if CHANGELOG_SOURCE.exists():
        docs.append(SourceDoc(CHANGELOG_SOURCE, CHANGELOG_TARGET))
    return docs


def target_for_deleted_source(rel: str) -> Path | None:
    if rel == "README.zh-CN.md":
        return README_TARGET
    if rel == "CHANGELOG.md":
        return CHANGELOG_TARGET
    prefix = "docs/"
    if rel.startswith(prefix) and rel.endswith(".md"):
        return TARGET_DIR / rel.removeprefix(prefix)
    return None


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


def code_language(info: str) -> str:
    parts = info.strip().split(maxsplit=1)
    if not parts:
        return ""
    return parts[0].lower()


def supports_code_comment_translation(language: str) -> bool:
    return language in {"bash", "sh", "shell", "toml", "yaml", "yml"}


def find_unquoted_hash(text: str) -> int:
    quote = ""
    escaped = False
    for index, char in enumerate(text):
        if escaped:
            escaped = False
            continue
        if char == "\\":
            escaped = True
            continue
        if quote:
            if char == quote:
                quote = ""
            continue
        if char in {'"', "'"}:
            quote = char
            continue
        if char == "#":
            return index
    return -1


def split_code_comment(line: str) -> tuple[str, str, str, str] | None:
    newline = "\n" if line.endswith("\n") else ""
    body = line[:-1] if newline else line
    hash_index = find_unquoted_hash(body)
    if hash_index < 0:
        return None
    prefix = body[:hash_index]
    comment = body[hash_index + 1 :]
    spacing = comment[: len(comment) - len(comment.lstrip())]
    text = comment[len(spacing) :]
    if not should_translate_text(text):
        return None
    return prefix, spacing, text, newline


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
        rel = source.relative_to(ROOT).as_posix()
        lines = source.read_text(encoding="utf-8").splitlines(keepends=True)
        rendered: list[str] = [AUTO_HEADER_TEMPLATE.format(source=rel)]
        in_code = False
        code_fence = ""
        code_lang = ""
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
                in_code, code_fence, code_lang = self.next_code_state(
                    fence_match,
                    in_code,
                    code_fence,
                    code_lang,
                )
                rendered.append(line)
                continue

            if in_code:
                rendered.append(self.render_code_line(rel, code_lang, line))
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

    @staticmethod
    def next_code_state(
        fence_match: re.Match[str],
        in_code: bool,
        code_fence: str,
        code_lang: str,
    ) -> tuple[bool, str, str]:
        fence = fence_match.group(1)
        if not in_code:
            return True, fence, code_language(fence_match.group(2))
        if fence == code_fence:
            return False, "", ""
        return in_code, code_fence, code_lang

    def render_code_line(self, rel: str, language: str, line: str) -> str:
        if not supports_code_comment_translation(language):
            return line
        return self.render_code_comment_line(rel, language, line)

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

    def render_code_comment_line(self, rel: str, language: str, line: str) -> str:
        parts = split_code_comment(line)
        if parts is None:
            return line
        prefix, spacing, text, newline = parts
        kind = f"code-comment:{language}"
        return f"{prefix}#{spacing}{self.render_plain_text(rel, kind, text).strip()}{newline}"

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
            self.cache_entries[segment.key] = cached
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

    def translate_batch(self, segments: list[PendingSegment], label: str = "batch") -> dict[str, str]:
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
                    log(f"    {label} failed, retrying {attempt + 1}/{self.retries} after {delay}s: {exc}")
                    time.sleep(delay)
                    continue
        raise RuntimeError(f"{label} translation failed: {last_error}")

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
        original = content
        content = normalize_json_response(content)
        try:
            parsed = json.loads(content)
        except json.JSONDecodeError as exc:
            preview = response_preview(original)
            raise ValueError(f"invalid JSON response: {exc}; preview={preview}") from exc
        if not isinstance(parsed, list):
            raise ValueError(f"model response is not a JSON array; preview={response_preview(original)}")
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


def is_zero_ref(ref: str) -> bool:
    ref = ref.strip()
    return bool(ref) and set(ref) == {"0"}


def stale_source_names(docs: list[SourceDoc], caches: dict[str, dict]) -> set[str]:
    stale: set[str] = set()
    for doc in docs:
        cache = caches[doc.source_rel]
        source_hash = sha256_text(doc.source.read_text(encoding="utf-8"))
        if cache.get("source_hash") != source_hash or not doc.target.exists():
            stale.add(doc.source_rel)
    return stale


def changed_source_names(
    args: argparse.Namespace,
    docs: list[SourceDoc],
    caches: dict[str, dict],
) -> set[str] | None:
    if not args.changed_only:
        return None
    stale = stale_source_names(docs, caches)
    if stale:
        log(f"including stale translated source(s): {', '.join(sorted(stale))}")
    if not args.base_ref or not args.head_ref or is_zero_ref(args.base_ref):
        log("changed-only requested but git refs are incomplete; falling back to full scan")
        return None
    try:
        result = subprocess.run(
            ["git", "diff", "--name-only", args.base_ref, args.head_ref, "--", "README.zh-CN.md", "CHANGELOG.md", "docs"],
            cwd=ROOT,
            check=True,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
    except (OSError, subprocess.CalledProcessError) as exc:
        log(f"git diff failed; falling back to full scan: {exc}")
        return None
    changed = {line.strip().replace("\\", "/") for line in result.stdout.splitlines() if line.strip()}
    git_changed = {
        name
        for name in changed
        if name == "README.zh-CN.md"
        or name == "CHANGELOG.md"
        or (name.startswith("docs/") and name.endswith(".md"))
    }
    return git_changed | stale


def select_source_docs(
    args: argparse.Namespace,
    docs: list[SourceDoc],
    caches: dict[str, dict],
) -> tuple[list[SourceDoc], set[str], set[str] | None]:
    current_sources = {doc.source_rel for doc in docs}
    changed = changed_source_names(args, docs, caches)
    if changed is None:
        return docs, current_sources, None
    selected = [doc for doc in docs if doc.source_rel in changed]
    return selected, current_sources, changed


def prune_deleted_sources(current_sources: set[str]) -> list[Path]:
    deleted_paths: list[Path] = []
    if not CACHE_DIR.exists():
        return deleted_paths
    for cache_path in sorted(CACHE_DIR.glob("**/*.json")):
        rel = source_for_cache_path(cache_path)
        if rel in current_sources:
            continue
        target = target_for_deleted_source(rel)
        if target is not None and target.exists() and target != README_TARGET:
            ensure_generated_path(target)
            target.unlink()
            deleted_paths.append(target)
        ensure_generated_path(cache_path)
        cache_path.unlink()
        deleted_paths.append(cache_path)
    return deleted_paths


def translate_batch_parallel(
    client: TranslationClient,
    batches: list[list[PendingSegment]],
    args: argparse.Namespace,
) -> dict[str, str]:
    out: dict[str, str] = {}
    workers = min(args.parallel_batches, len(batches))
    with ThreadPoolExecutor(max_workers=workers) as executor:
        futures = {}
        for index, batch in enumerate(batches, start=1):
            log(f"  scheduling batch {index}/{len(batches)} ({len(batch)} segment(s))")
            label = f"batch {index}/{len(batches)}"
            future = executor.submit(client.translate_batch, batch, label)
            futures[future] = index
            if args.batch_delay > 0 and index < len(batches):
                time.sleep(args.batch_delay)
        for future in as_completed(futures):
            index = futures[future]
            out.update(future.result())
            log(f"  completed batch {index}/{len(batches)}")
    return out


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
    translations: dict[str, str] = {}
    if args.parallel_batches > 1 and len(batches) > 1:
        translations = translate_batch_parallel(client, batches, args)
    else:
        for index, batch in enumerate(batches, start=1):
            log(f"  translating batch {index}/{len(batches)} ({len(batch)} segment(s))")
            translations.update(client.translate_batch(batch, f"batch {index}/{len(batches)}"))
            if args.batch_delay > 0 and index < len(batches):
                log(f"  waiting {args.batch_delay:g}s before next batch")
                time.sleep(args.batch_delay)
    for item in pending:
        translation = translations[item.segment.key]
        rendered = rendered.replace(item.placeholder, unmask_inline_code(translation, item.masks))
        cache_entries[item.segment.key] = cache_entry(item, translation)
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


def run_git(args: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(["git", *args], cwd=ROOT, check=True, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)


def has_staged_changes(paths: list[Path]) -> bool:
    rel_paths = [path.relative_to(ROOT).as_posix() for path in paths]
    result = subprocess.run(["git", "diff", "--cached", "--quiet", "--", *rel_paths], cwd=ROOT)
    return result.returncode == 1


def is_allowed_generated_path(path: Path) -> bool:
    resolved = path.resolve()
    target_root = TARGET_DIR.resolve()
    return (
        resolved == README_TARGET.resolve()
        or resolved == CHANGELOG_TARGET.resolve()
        or resolved == LEGACY_CACHE_PATH.resolve()
        or resolved == CACHE_DIR.resolve()
        or CACHE_DIR.resolve() in resolved.parents
        or resolved == target_root
        or target_root in resolved.parents
    )


def ensure_generated_path(path: Path) -> None:
    if not is_allowed_generated_path(path):
        rel = path.relative_to(ROOT).as_posix() if path.is_relative_to(ROOT) else str(path)
        raise ValueError(f"refusing to write non-generated path: {rel}")


def write_text_file(path: Path, content: str) -> None:
    ensure_generated_path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8", newline="\n") as f:
        f.write(content)


def commit_translation(paths: list[Path], message: str) -> None:
    for path in paths:
        ensure_generated_path(path)
    rel_paths = [path.relative_to(ROOT).as_posix() for path in paths]
    log(f"  staging: {', '.join(rel_paths)}")
    run_git(["add", *rel_paths])
    if not has_staged_changes(paths):
        log("  no git changes to commit")
        return
    log(f"  committing: {message}")
    run_git(["commit", "-m", message, "--", *rel_paths])
    log("  pushing translated output")
    run_git(["push"])
    log(f"  committed: {message}")


def rewrite_readme_language_switcher(rendered: str) -> str:
    lines = rendered.splitlines()
    for index, line in enumerate(lines[:8]):
        normalized = line.strip().lower()
        if normalized in {"chinese | [english](readme.md)", "中文 | [english](readme.md)"}:
            lines[index] = "[中文](README.zh-CN.md) | English"
            return "\n".join(lines) + ("\n" if rendered.endswith("\n") else "")
    return rendered


def rewrite_readme_feature_numbering(rendered: str) -> str:
    roman = {1: "I", 2: "II", 3: "III", 4: "IV", 5: "V", 6: "VI", 7: "VII", 8: "VIII", 9: "IX", 10: "X"}
    lines = rendered.splitlines()
    in_features = False
    for index, line in enumerate(lines):
        if line.strip().lower() == "## features":
            in_features = True
            continue
        if in_features and line.startswith("## "):
            break
        if not in_features:
            continue
        match = re.match(r"^(###\s+)(\d{1,2})([.)]\s+)(.*)$", line)
        if match and int(match.group(2)) in roman:
            lines[index] = f"{match.group(1)}{roman[int(match.group(2))]}. {match.group(4)}"
    return "\n".join(lines) + ("\n" if rendered.endswith("\n") else "")


def rewrite_readme_links(rendered: str) -> str:
    rendered = rewrite_readme_language_switcher(rendered)
    rendered = rewrite_readme_feature_numbering(rendered)
    return re.sub(r"\((docs/[^)\s]+\.md(?:#[^)\s]+)?)\)", r"(docs.en/\1)", rendered).replace("docs.en/docs/", "docs.en/")



def finalize_rendered(doc: SourceDoc, rendered: str) -> str:
    if doc.target == README_TARGET:
        return rewrite_readme_links(rendered)
    return rendered


def make_doc_cache(doc: SourceDoc, previous: dict, segments: dict[str, dict]) -> dict:
    source_hash = sha256_text(doc.source.read_text(encoding="utf-8"))
    unchanged = (
        previous.get("version") == 2
        and previous.get("source") == doc.source_rel
        and previous.get("target") == doc.target_rel
        and previous.get("source_hash") == source_hash
        and previous.get("segments") == segments
    )
    return {
        "version": 2,
        "source": doc.source_rel,
        "target": doc.target_rel,
        "source_hash": source_hash,
        "updated_at": previous.get("updated_at", 0) if unchanged else int(time.time()),
        "segments": segments,
    }


def run(args: argparse.Namespace) -> int:
    docs = all_source_docs()
    migration_paths = migrate_legacy_cache(docs)
    if migration_paths and args.commit_each and not args.dry_run:
        commit_translation(migration_paths, "docs: migrate translation cache")

    caches = {doc.source_rel: read_doc_cache(doc) for doc in docs}
    sources, current_source_names, changed_names = select_source_docs(args, docs, caches)
    deleted_paths = prune_deleted_sources(current_source_names)
    if deleted_paths:
        if args.commit_each and not args.dry_run:
            commit_translation(deleted_paths, "docs: remove translated deleted sources")

    if changed_names is not None:
        deleted = sorted(changed_names - current_source_names)
        if deleted:
            log(f"deleted source file(s): {', '.join(deleted)}")
    log(f"found {len(sources)} source doc file(s) to process")
    client: TranslationClient | None = None
    staged_outputs: dict[Path, str] = {}
    staged_caches: dict[Path, dict] = {}
    total_pending = 0
    total_reused = 0

    for source_index, doc in enumerate(sources, start=1):
        rel = doc.source_rel
        log(f"[{source_index}/{len(sources)}] rendering {rel} -> {doc.target_rel}")
        cache = caches[rel]
        renderer = MarkdownRenderer(cache=cache)
        result = renderer.render_file(doc.source)
        total_pending += len(result.pending)
        total_reused += result.reused_count
        log(
            f"  segments: reused={result.reused_count}, "
            f"changed={len(result.pending)}, skipped={result.skipped_count}"
        )

        if result.pending and not args.dry_run and client is None:
            client = make_client(args)
        rendered = translate_pending(result.text, result.pending, result.cache_entries, client, args, rel)
        rendered = finalize_rendered(doc, rendered)
        staged_outputs[doc.target] = rendered
        cache_path = cache_path_for_source(rel)
        new_cache = make_doc_cache(doc, cache, result.cache_entries)
        staged_caches[cache_path] = new_cache
        if args.commit_each and not args.dry_run:
            write_text_file(doc.target, rendered)
            write_json(cache_path, new_cache)
            commit_translation([doc.target, cache_path], f"docs: auto-translate {rel}")

    if not args.commit_each or args.dry_run:
        TARGET_DIR.mkdir(parents=True, exist_ok=True)
        for target, rendered in staged_outputs.items():
            write_text_file(target, rendered)
        for cache_path, cache in staged_caches.items():
            write_json(cache_path, cache)

    if args.dry_run:
        log("dry run completed; changed segments were copied from source text")
    else:
        log(f"translated {len(staged_outputs)} file(s)")
    log(f"summary: reused={total_reused}, changed={total_pending}, batch_size={args.batch_size}")
    return 0


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Incrementally translate docs/ to docs.en/.")
    parser.add_argument("--dry-run", action="store_true", help="Do not call the LLM; copy source text for changed segments.")
    parser.add_argument("--changed-only", action="store_true", help="Use git diff to process only changed source files.")
    parser.add_argument("--commit-each", action="store_true", help="Commit and push each translated source document immediately.")
    parser.add_argument("--base-ref", default="", help="Base git ref for --changed-only.")
    parser.add_argument("--head-ref", default="", help="Head git ref for --changed-only.")
    parser.add_argument("--timeout", type=int, default=240, help="HTTP timeout in seconds.")
    parser.add_argument("--retries", type=int, default=5, help="Retry count per batch.")
    parser.add_argument("--batch-size", type=int, default=8, help="Changed segment count per translation request.")
    parser.add_argument("--parallel-batches", type=int, default=1, help="Concurrent translation batches per source document.")
    parser.add_argument("--batch-delay", type=float, default=5.0, help="Delay between translation batches in seconds.")
    args = parser.parse_args(argv)
    if args.batch_size < 1:
        parser.error("--batch-size must be at least 1")
    if args.parallel_batches < 1:
        parser.error("--parallel-batches must be at least 1")
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
