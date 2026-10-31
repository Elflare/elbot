#!/usr/bin/env python3
"""Incrementally translate user-facing Markdown docs from docs/ to docs.en/.

Chinese docs in docs/ are the source of truth. This script generates an English
mirror under docs.en/ and stores one per-section translation cache file per
source document under the repository-local `.translation-cache/` directory.

Only changed Markdown sections are sent to an OpenAI-compatible chat completions
API. Sections end at headings through level three. Code is protected except
selected documentation comments. Automatic publishing reconciles each document
against the latest upstream source before committing generated files.
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

HEADING_RE = re.compile(r"^(\s{0,3}#{1,6}\s+)(.*?)(\s+#+\s*)?$")
HTML_COMMENT_RE = re.compile(r"^\s*<!--.*-->\s*$")
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
        # Parent headings distinguish identical sections in different contexts;
        # positions are excluded so inserting a preceding section keeps cache hits.
        return f"{self.source_file}:{self.kind}:{sha256_text(self.context + chr(0) + self.text)}"

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
    # Hash of the exact complete source text read for this rendering.
    source_hash: str


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
        "version": 3,
        "source": doc.source_rel,
        "target": doc.target_rel,
        "source_hash": "",
        "updated_at": 0,
        "segments": {},
    }


def read_doc_cache(doc: SourceDoc) -> dict:
    cache = read_json(cache_path_for_source(doc.source_rel), empty_doc_cache(doc))
    if cache.get("version") != 3 or cache.get("source") != doc.source_rel:
        return empty_doc_cache(doc)
    if not isinstance(cache.get("segments"), dict):
        cache["segments"] = {}
    return cache


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


def split_sections(text: str) -> list[tuple[str, str]]:
    """Return lossless Markdown sections and their actual parent headings."""
    sections: list[tuple[str, str]] = []
    headings: list[tuple[int, str]] = []
    lines: list[str] = []
    context = ""
    fence = ""
    in_comment = False
    for line in text.splitlines(keepends=True):
        marker = re.match(r"^ {0,3}(`{3,}|~{3,})(.*)$", line.rstrip("\n"))
        if fence:
            lines.append(line)
            if marker and marker[1][0] == fence[0] and len(marker[1]) >= len(fence) and not marker[2].strip():
                fence = ""
            continue
        if in_comment:
            lines.append(line)
            in_comment = "-->" not in line
            continue
        if marker:
            fence = marker[1]
        elif "<!--" in line:
            in_comment = "-->" not in line.partition("<!--")[2]
        else:
            heading = HEADING_RE.match(line.rstrip("\n"))
            if heading:
                level = len(heading[1].strip())
                if level <= 3:
                    if lines:
                        sections.append(("".join(lines), context))
                    lines = []
                    headings = [(depth, title) for depth, title in headings if depth < level]
                    context = " > ".join(title for _, title in headings)
                    headings.append((level, heading[2].strip()))
        lines.append(line)
    if lines:
        sections.append(("".join(lines), context))
    return sections


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


def unmask_inline_code(text: str, masks: dict[str, str]) -> str:
    for token, value in masks.items():
        text = text.replace(token, value)
    return text


def mask_markdown(text: str) -> tuple[str, dict[str, str]]:
    """Protect executable code and Markdown destinations in a complete section."""
    masks: dict[str, str] = {}

    def protect(value: str) -> str:
        token = f"__ELBOT_CODE_{len(masks)}__"
        masks[token] = value
        return token

    text = re.sub(r"<!--.*?-->", lambda match: protect(match[0]), text, flags=re.DOTALL)
    lines: list[str] = []
    fence = ""
    language = ""
    for line in text.splitlines(keepends=True):
        body = line.removesuffix("\n")
        newline = "\n" if line.endswith("\n") else ""
        marker = re.match(r"^ {0,3}(`{3,}|~{3,})(.*)$", body)
        if marker and (not fence or (marker[1][0] == fence[0] and len(marker[1]) >= len(fence) and not marker[2].strip())):
            if fence:
                fence = ""
            else:
                fence = marker[1]
                language = code_language(marker[2])
            lines.append(protect(body) + newline)
            continue
        if fence:
            comment = split_code_comment(line) if supports_code_comment_translation(language) else None
            if comment:
                prefix, spacing, body, newline = comment
                body = protect(prefix + "#" + spacing) + re.sub(
                    r"(`+)(.*?)\1", lambda match: protect(match[0]), body,
                )
                lines.append(body + newline)
            else:
                lines.append((protect(body) if body else "") + newline)
            continue
        body = re.sub(r"(`+)(.*?)\1", lambda match: protect(match[0]), body)
        body = re.sub(r"(?<=\]\()[^)\n]*(?=\))", lambda match: protect(match[0]), body)
        body = re.sub(r"https?://[^\s<>)]+", lambda match: protect(match[0]), body)
        lines.append(body + newline)
    return "".join(lines), masks


def chunked(items: list[PendingSegment], size: int) -> list[list[PendingSegment]]:
    return [items[i : i + size] for i in range(0, len(items), size)]


class MarkdownRenderer:
    def __init__(self, cache: dict) -> None:
        self.cache = cache

    def render_file(self, source: Path) -> RenderResult:
        rel = source.relative_to(ROOT).as_posix()
        source_text = source.read_text(encoding="utf-8")
        rendered: list[str] = [AUTO_HEADER_TEMPLATE.format(source=rel)]
        pending: list[PendingSegment] = []
        entries: dict[str, dict] = {}
        reused = skipped = 0
        for index, (text, context) in enumerate(split_sections(source_text)):
            segment = Segment(rel, index, "section", text, context)
            masked, masks = mask_markdown(text)
            if not should_translate_text(masked):
                skipped += 1
                rendered.append(text)
                continue
            cached = self.cache.get("segments", {}).get(segment.key)
            if cached and cached.get("source_hash") == segment.hash:
                reused += 1
                entries[segment.key] = cached
                rendered.append(cached["translation"])
                continue
            placeholder = PLACEHOLDER_TEMPLATE.format(index=index)
            pending.append(PendingSegment(segment, masks, placeholder))
            rendered.append(placeholder)
        return RenderResult(
            "".join(rendered), pending, entries, reused, skipped, sha256_text(source_text),
        )


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
            "instruction": "Translate each complete Markdown section in text. source_markdown is read-only context. Return JSON only.",
            "items": [
                {
                    "key": item.segment.key,
                    "source_file": item.segment.source_file,
                    "markdown_kind": item.segment.kind,
                    "heading_context": item.segment.context,
                    "source_markdown": item.segment.text,
                    "text": mask_markdown(item.segment.text)[0],
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
            "- Translate each entire section coherently, including its headings, lists and tables.\n"
            "- Use heading_context as the parent heading hierarchy.\n"
            "- Translate text, using source_markdown only to understand the original code and context.\n"
            "- Preserve Markdown structure and every placeholder like __ELBOT_CODE_0__ exactly once.\n"
            "- Preserve commands, paths, config keys, environment variable names, URLs, and product names.\n"
            "- Executable code is protected; translate only the exposed documentation comments.\n"
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
        expected = {item.segment.key: item for item in segments}
        out: dict[str, str] = {}
        for item in parsed:
            if not isinstance(item, dict):
                raise ValueError("model response item is not an object")
            key = str(item.get("key", ""))
            translation = str(item.get("translation", ""))
            if key not in expected:
                raise ValueError(f"unexpected translation key: {key}")
            section = expected[key]
            source = section.segment.text
            for token in section.masks:
                if translation.count(token) != 1:
                    raise ValueError(f"protected Markdown token must occur exactly once: {token}")
            prefix = source[: len(source) - len(source.lstrip("\n"))]
            suffix = source[len(source.rstrip("\n")) :]
            translation = prefix + translation.strip("\n") + suffix
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
            try:
                out.update(future.result())
            except RuntimeError as exc:
                log(f"warning: {batches[index - 1][0].segment.source_file}, batch {index}/{len(batches)}: {exc}; skipping batch")
                continue
            log(f"  completed batch {index}/{len(batches)}")
    return out


def translate_pending(
    rendered: str,
    pending: list[PendingSegment],
    cache_entries: dict[str, dict],
    client: TranslationClient | None,
    args: argparse.Namespace,
    rel: str,
) -> str | None:
    """Cache successful segments; return None while any batch remains incomplete."""
    if not pending:
        return rendered

    if args.dry_run:
        for item in pending:
            translation = item.segment.text
            rendered = rendered.replace(item.placeholder, translation)
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
            try:
                translations.update(client.translate_batch(batch, f"batch {index}/{len(batches)}"))
            except RuntimeError as exc:
                log(f"warning: {rel}, batch {index}/{len(batches)}: {exc}; skipping batch")
            if args.batch_delay > 0 and index < len(batches):
                log(f"  waiting {args.batch_delay:g}s before next batch")
                time.sleep(args.batch_delay)
    failed_count = 0
    for item in pending:
        if item.segment.key not in translations:
            failed_count += 1
            continue
        translation = unmask_inline_code(translations[item.segment.key], item.masks)
        rendered = rendered.replace(item.placeholder, translation)
        cache_entries[item.segment.key] = cache_entry(item, translation)
    return None if failed_count else rendered


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
    try:
        return subprocess.run(
            ["git", *args], cwd=ROOT, check=True, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            env={**os.environ, "LC_ALL": "C"},
        )
    except subprocess.CalledProcessError as exc:
        if exc.stdout:
            print(exc.stdout.rstrip(), file=sys.stderr)
        if exc.stderr:
            print(exc.stderr.rstrip(), file=sys.stderr)
        raise


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


def sync_upstream() -> None:
    run_git(["fetch"])
    run_git(["merge", "--ff-only", "@{upstream}"])


def commit_translation(paths: list[Path], message: str) -> bool:
    for path in paths:
        ensure_generated_path(path)
    rel_paths = [path.relative_to(ROOT).as_posix() for path in paths]
    log(f"  staging: {', '.join(rel_paths)}")
    run_git(["add", *rel_paths])
    if not has_staged_changes(paths):
        log("  no git changes to commit")
        return True
    log(f"  committing: {message}")
    run_git(["commit", "-m", message, "--", *rel_paths])
    log("  pushing translated output")
    try:
        run_git(["push", "--porcelain"])
    except subprocess.CalledProcessError as exc:
        if re.search(
            r"^!\t[^\n]*\t\[rejected\] \((?:fetch first|non-fast-forward)\)$",
            exc.stdout or "", re.MULTILINE,
        ):
            return False
        raise
    log(f"  committed: {message}")
    return True


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


def make_doc_cache(doc: SourceDoc, previous: dict, segments: dict[str, dict], source_hash: str) -> dict:
    unchanged = (
        previous.get("version") == 3
        and previous.get("source") == doc.source_rel
        and previous.get("target") == doc.target_rel
        and previous.get("source_hash") == source_hash
        and previous.get("segments") == segments
    )
    return {
        "version": 3,
        "source": doc.source_rel,
        "target": doc.target_rel,
        "source_hash": source_hash,
        "updated_at": previous.get("updated_at", 0) if unchanged else int(time.time()),
        "segments": segments,
    }


def translate_document(
    doc: SourceDoc,
    args: argparse.Namespace,
    client: TranslationClient | None,
    attempted_versions: set[tuple[str, str]],
) -> tuple[TranslationClient | None, int, int, bool]:
    """Translate snapshots in memory; publish only against the matching upstream source."""
    publishing = args.commit_each and not args.dry_run
    segments: dict[str, dict] = {}
    total_pending = total_reused = push_attempts = 0
    while True:
        if publishing:
            sync_upstream()
        if not doc.source.exists():
            return client, total_pending, total_reused, False
        cache = read_doc_cache(doc)
        segments.update(cache["segments"])
        result = MarkdownRenderer({"segments": segments}).render_file(doc.source)
        version = (doc.source_rel, result.source_hash)
        log(f"rendering {doc.source_rel}: sections reused={result.reused_count}, changed={len(result.pending)}")
        if version not in attempted_versions:
            attempted_versions.add(version)
            total_pending += len(result.pending)
            total_reused += result.reused_count
            if result.pending and not args.dry_run and client is None:
                client = make_client(args)
            rendered = translate_pending(result.text, result.pending, result.cache_entries, client, args, doc.source_rel)
            segments.update(result.cache_entries)
        else:
            rendered = None if result.pending else result.text

        if publishing:
            sync_upstream()
            if not doc.source.exists():
                return client, total_pending, total_reused, False
            if sha256_text(doc.source.read_text(encoding="utf-8")) != result.source_hash:
                log(f"  source changed: {doc.source_rel}; reusing unchanged sections")
                continue
            cache = read_doc_cache(doc)
            segments.update(cache["segments"])
            result = MarkdownRenderer({"segments": segments}).render_file(doc.source)
            rendered = None if result.pending else result.text

        new_cache = make_doc_cache(doc, cache, result.cache_entries, result.source_hash)
        cache_path = cache_path_for_source(doc.source_rel)
        paths = [cache_path]
        incomplete = rendered is None
        base = run_git(["rev-parse", "HEAD"]).stdout.strip() if publishing else ""
        if incomplete:
            new_cache["source_hash"] = ""
            log(f"warning: {doc.source_rel}: incomplete; preserving target and saving complete sections")
            message = f"docs: cache partial translation for {doc.source_rel}"
        else:
            write_text_file(doc.target, finalize_rendered(doc, rendered))
            paths.insert(0, doc.target)
            message = f"docs: auto-translate {doc.source_rel}"
        write_json(cache_path, new_cache)
        if not publishing or commit_translation(paths, message):
            return client, total_pending, total_reused, incomplete
        push_attempts += 1
        if push_attempts == 3:
            raise RuntimeError(f"remote kept advancing while publishing {doc.source_rel}; generated commit retained")
        log(f"  remote advanced; reconciling before push retry {push_attempts}/2")
        # Startup establishes an exclusive clean worktree. Only our unpublished
        # generated commit exists above base; successful translations stay in memory.
        run_git(["reset", "--hard", base])


def run(args: argparse.Namespace) -> int:
    publishing = args.commit_each and not args.dry_run
    if publishing:
        if run_git(["status", "--porcelain"]).stdout.strip():
            raise ValueError("--commit-each requires a clean dedicated worktree")
        sync_upstream()
        if run_git(["rev-list", "@{upstream}..HEAD"]).stdout.strip():
            raise ValueError("--commit-each requires a branch without unpublished commits")

    client: TranslationClient | None = None
    attempted_versions: set[tuple[str, str]] = set()
    total_pending = total_reused = failed_count = translated = cleanup_attempts = 0
    first_pass = True
    while True:
        if publishing:
            sync_upstream()
        elif not first_pass:
            break
        docs = all_source_docs()
        base = run_git(["rev-parse", "HEAD"]).stdout.strip() if publishing else ""
        deleted_paths = prune_deleted_sources({doc.source_rel for doc in docs})
        if deleted_paths and publishing:
            if commit_translation(deleted_paths, "docs: remove translated deleted sources"):
                cleanup_attempts = 0
                continue
            cleanup_attempts += 1
            if cleanup_attempts == 3:
                raise RuntimeError("remote kept advancing while removing deleted sources; generated commit retained")
            run_git(["reset", "--hard", base])
            continue
        caches = {doc.source_rel: read_doc_cache(doc) for doc in docs}
        if first_pass:
            sources, _, _ = select_source_docs(args, docs, caches)
            first_pass = False
        else:
            stale = stale_source_names(docs, caches)
            sources = [doc for doc in docs if doc.source_rel in stale]
        sources = [doc for doc in sources if (
            doc.source_rel, sha256_text(doc.source.read_text(encoding="utf-8")),
        ) not in attempted_versions]
        if not sources:
            break
        for doc in sources:
            client, pending, reused, incomplete = translate_document(doc, args, client, attempted_versions)
            total_pending += pending
            total_reused += reused
            failed_count += incomplete
            translated += not incomplete and doc.source.exists()
            if publishing:
                break  # Re-discover sources and caches after every publication.
    log(f"translated {translated} file(s)")
    log(f"summary: reused={total_reused}, changed={total_pending}, incomplete={failed_count}, batch_size={args.batch_size}")
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
    parser.add_argument("--batch-size", type=int, default=1, help="Complete Markdown section count per translation request.")
    parser.add_argument("--parallel-batches", type=int, default=2, help="Concurrent translation batches per source document.")
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
