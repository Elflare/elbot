#!/usr/bin/env python3
"""Translate complete Markdown sections and publish them from a workflow checkout."""

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
WORKERS = 2
RETRIES = 10
REQUEST_TIMEOUT = 240
HEADING_RE = re.compile(r"^( {0,3}#{1,6}\s+)(.*?)(\s+#+\s*)?$")
CODE_FENCE_RE = re.compile(r"^ {0,3}(`{3,}|~{3,})(.*)$")
DEFAULT_GLOSSARY = {
    "chat 模式": "chat mode", "work 模式": "work mode",
    "工具发现": "tool discovery", "常驻记忆": "resident memory",
    "长期记忆": "long-term memory", "上下文压缩": "context compaction",
    "平台适配": "platform adapter", "输出意图": "output intent",
    "审计日志": "audit log", "运行日志": "runtime log",
    "超级管理员": "superadmin", "高风险": "high-risk", "低风险": "low-risk",
    "命令": "command", "会话": "Session", "工具": "tool", "技能": "Skill",
    "原生 skill": "native skill", "后台 LLM": "background LLM",
    "后台 Session": "background Session", "监听枢纽": "listening hub",
    "外部事件": "external event", "事件协议": "event protocol",
    "事件模式": "event mode", "投递事件": "deliver events",
    "投递目标": "delivery target", "安全边界": "security boundary",
    "任务表示法": "Task Notation", "艾露妮斯": "Elnis",
    "艾露维娜": "Elvena", "艾露维丝": "Elwisp",
}


def sha256_text(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def split_manual_header(text: str) -> tuple[str, str]:
    """Return the exact hand-maintained README prefix and the translatable body."""
    start, end = "<!-- manual-header:start -->", "<!-- manual-header:end -->"
    if text.count(start) != 1 or text.count(end) != 1 or text.index(start) >= text.index(end):
        raise ValueError("README requires one ordered manual-header:start / manual-header:end pair")
    prefix = text[:text.index(start)].strip()
    if prefix and not re.fullmatch(r"<!-- (?:This file is auto-translated|Body auto-translated).*? -->", prefix):
        raise ValueError("manual-header must be at the start of README, after its generation notice")
    boundary = text.index(end) + len(end)
    boundary += len(text[boundary:]) - len(text[boundary:].lstrip("\r\n"))
    if not text[boundary:].startswith("## "):
        raise ValueError("README body must start with a level-two heading after manual-header:end")
    return text[:boundary], text[boundary:]


def protected_markup(text: str) -> list[str]:
    """Extract structural fragments and links; leave prose out of validation."""
    tokens, brackets = [], []
    pattern = re.compile(
        r"(?P<code>(`+).*?\2)|(?P<escape>\\.)|(?P<footnote>\[\^[^\]\n]+\])|"
        r"(?P<definition>^ {0,3}\[[^\]\n]+\]:[^\n]*)|"
        r"(?P<html></?[A-Za-z][\w:-]*(?:\s+(?:[^<>\"']|\"[^\"]*\"|'[^']*')*)?\s*/?>)|"
        r"(?P<open>!?\[)|(?P<close>\])|(?P<url>https?://[^\s<>)]+)", re.DOTALL | re.MULTILINE)
    offset = 0
    while match := pattern.search(text, offset):
        offset = match.end()
        kind = match.lastgroup
        if kind == "escape":
            continue
        if kind == "open":
            brackets.append((len(tokens), match[0], offset))
            tokens.append("")
        elif kind == "close" and brackets:
            index, opening, start = brackets.pop()
            if offset < len(text) and text[offset] == "(":
                depth, end = 1, offset + 1
                while end < len(text) and depth:
                    if text[end] == "\\":
                        end += 2
                        continue
                    if text[end] == "(":
                        depth += 1
                    elif text[end] == ")":
                        depth -= 1
                    end += 1
                if depth == 0:
                    tokens[index] = opening
                    tokens.append("]" + text[offset:end])
                    offset = end
            elif reference := re.match(r"\[[^\]\n]*\]", text[offset:]):
                tokens[index] = opening
                tokens.append("]" + (reference[0] if reference[0] != "[]" else "[" + text[start:match.start()] + "]"))
                offset += reference.end()
        elif kind in {"html", "code", "url", "footnote", "definition"}:
            tokens.append(match[0])
    return [token for token in tokens if token]


@dataclass(frozen=True)
class Segment:
    source_file: str
    text: str
    context: str

    @property
    def key(self) -> str:
        # Keep existing v3 keys: parent context distinguishes identical sections.
        return f"{self.source_file}:section:{sha256_text(self.context + chr(0) + self.text)}"


@dataclass(frozen=True)
class SourceDoc:
    source: Path
    target: Path
    # Immutable source snapshot; publishing checks this exact version.
    text: str

    @property
    def source_rel(self) -> str:
        return self.source.relative_to(ROOT).as_posix()

    @property
    def source_hash(self) -> str:
        return sha256_text(self.text)


def all_source_docs() -> list[SourceDoc]:
    paths = [(path, TARGET_DIR / path.relative_to(SOURCE_DIR)) for path in sorted(SOURCE_DIR.rglob("*.md"))]
    paths += [(source, target) for source, target in [
        (README_SOURCE, README_TARGET), (CHANGELOG_SOURCE, CHANGELOG_TARGET),
    ] if source.exists()]
    docs = [SourceDoc(source, target, source.read_text(encoding="utf-8")) for source, target in paths]
    for doc in docs:
        if doc.source == README_SOURCE:
            split_manual_header(doc.text)
            if not doc.target.exists():
                raise ValueError("README.md is missing; create its English manual-header before translating")
            split_manual_header(doc.target.read_text(encoding="utf-8"))
    return docs


def split_sections(text: str) -> list[tuple[str, str]]:
    """Split through heading level three without changing source bytes or hierarchy."""
    sections, headings, lines = [], [], []
    context = fence = ""
    in_comment = False
    for line in text.splitlines(keepends=True):
        marker = CODE_FENCE_RE.match(line.rstrip("\n"))
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
            if heading and len(heading[1].strip()) <= 3:
                if lines:
                    sections.append(("".join(lines), context))
                lines = []
                level = len(heading[1].strip())
                headings = [(depth, title) for depth, title in headings if depth < level]
                context = " > ".join(title for _, title in headings)
                headings.append((level, heading[2].strip()))
        lines.append(line)
    if lines:
        sections.append(("".join(lines), context))
    return sections


def should_translate_text(text: str) -> bool:
    return bool(re.search(r"[\u4e00-\u9fff]", text))


def validate_translation(source: str, translation: str) -> None:
    """Check nonempty output, fragment types and link targets without comparing code text."""
    if not translation.strip():
        raise ValueError("empty section translation")

    def parts(text):
        out, fence, language = [], "", ""
        prose = []
        for line in re.sub(r"<!--.*?-->", "", text, flags=re.DOTALL).splitlines():
            marker = CODE_FENCE_RE.match(line)
            if marker and (not fence or (marker[1][0] == fence[0] and len(marker[1]) >= len(fence) and not marker[2].strip())):
                out.extend(("literal", token) for token in protected_markup("\n".join(prose)))
                prose = []
                out.append(("literal", line))
                if fence:
                    fence = ""
                else:
                    fence = marker[1]
                    language = marker[2].strip().split(maxsplit=1)[0].lower() if marker[2].strip() else ""
            elif fence:
                out.append(("code:" + language, line))
            else:
                prose.append(line)
        out.extend(("literal", token) for token in protected_markup("\n".join(prose)))
        links = []
        for kind, token in out:
            target = None
            if kind.startswith("code:") or token.startswith("`"):
                links.extend(("target", match[0]) for match in re.finditer(r"https?://[^\s<>`\"')]+", token))
            elif token.startswith("]("):
                target = token[2:-1]
            elif token.startswith("]["):
                links.append(("reference", token[2:-1]))
            elif token.startswith("[^"):
                links.append(("footnote", token[2:-1]))
            elif match := re.match(r"^ {0,3}\[([^\]\n]+)\]:[ \t]*(.*)", token):
                links.append(("reference", match[1]))
                target = match[2]
            elif token.startswith("<"):
                for match in re.finditer(r"""\s([\w:-]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>`]+))""", token):
                    if match[1].lower() in {"href", "src"}:
                        links.append(("target", next((value for value in match.groups()[1:] if value), "")))
            elif token.startswith(("http://", "https://")):
                target = token
            if target is not None:
                target = re.match(r"\s*(<[^>]*>|\S*)", target)[1]
                links.append(("target", target.removeprefix("<").removesuffix(">")))
        return [kind for kind, _ in out], sorted(links)

    original, translated = parts(source), parts(translation)
    if original[0] != translated[0]:
        raise ValueError("code block language or structure changed")
    if original[1] != translated[1]:
        raise ValueError("link destination or reference changed")


class TranslationClient:
    def __init__(self):
        self.api_key = os.environ["TRANSLATE_LLM_API_KEY"]
        self.endpoint = os.environ["TRANSLATE_LLM_BASE_URL"].rstrip("/") + "/chat/completions"
        self.model = os.environ["TRANSLATE_LLM_MODEL"]

    def translate_section(self, section: Segment) -> str:
        glossary = "\n".join(f"{source}: {target}" for source, target in DEFAULT_GLOSSARY.items())
        payload = {"model": self.model, "temperature": 0.2, "messages": [
            {"role": "system", "content": (
                "Translate ElBot documentation from Simplified Chinese to English. "
                "Return ONLY the translated Markdown section, without explanations or an enclosing code fence. "
                "Preserve Markdown structure, commands, identifiers, URLs and link destinations. "
                "Preserve image markers, nested links and valid HTML; visible text, link titles and "
                "HTML alt/title text may be translated. "
                "Translate Chinese prose, including descriptions, placeholders, comments and example text "
                "inside inline code and fenced code blocks in any language. Translate ordinary HTML comments too. "
                "Use natural English word order in descriptions and comments while keeping executable code valid. "
                "Preserve code fences, backtick delimiters and product names.\n"
                "Terminology:\n" + glossary
            )},
            {"role": "user", "content": (
                f"Source file: {section.source_file}\nParent headings: {section.context}\n\n{section.text}"
            )},
        ]}
        request = urllib.request.Request(self.endpoint, json.dumps(payload).encode(), headers={
            "Authorization": "Bearer " + self.api_key, "Content-Type": "application/json",
        })
        for attempt in range(RETRIES + 1):
            try:
                with urllib.request.urlopen(request, timeout=REQUEST_TIMEOUT) as response:
                    content = json.load(response)["choices"][0]["message"]["content"]
                prefix = section.text[:len(section.text) - len(section.text.lstrip("\n"))]
                suffix = section.text[len(section.text.rstrip("\n")):]
                try:
                    while content.lstrip().startswith("<thought>"):
                        if "</thought>" not in content:
                            raise ValueError("unclosed thought block")
                        content = content.split("</thought>", 1)[1]
                    validate_translation(section.text, prefix + content.strip("\n") + suffix)
                except ValueError as exc:
                    print(
                        f"validation failed: {section.source_file} | {section.context} | "
                        f"{section.text.strip().splitlines()[0]} | "
                        f"attempt {attempt + 1}/{RETRIES + 1}: {exc}\n"
                        f"--- LLM response ---\n{content}\n--- end LLM response ---",
                        flush=True,
                    )
                    raise
                return prefix + content.strip("\n") + suffix
            except urllib.error.HTTPError as exc:
                exc.close()
                if exc.code != 429 and not 500 <= exc.code < 600:
                    raise
                error = exc
            except (OSError, http.client.HTTPException, ValueError) as exc:
                error = exc
            if attempt < RETRIES:
                print(f"  retry {attempt + 1}/{RETRIES}: {error}", flush=True)
                time.sleep(min(2 ** attempt, 32))
        raise RuntimeError(f"section translation exhausted retries: {error}")


def cache_path_for_source(source: str) -> Path:
    return CACHE_DIR / (source + ".json")


def empty_doc_cache(doc: SourceDoc) -> dict:
    return {"version": 3, "source": doc.source_rel, "target": doc.target.relative_to(ROOT).as_posix(),
            "source_hash": "", "updated_at": 0, "segments": {}}


def cache_entry(section: Segment, translation: str) -> dict:
    return {"source_hash": sha256_text(section.text), "source": section.text, "translation": translation,
            "kind": "section", "source_file": section.source_file, "updated_at": int(time.time())}


def make_doc_cache(doc: SourceDoc, previous: dict, entries: dict, complete: bool) -> dict:
    source_hash = doc.source_hash if complete else ""
    if previous["source_hash"] == source_hash and previous["segments"] == entries:
        return previous
    return {**empty_doc_cache(doc), "source_hash": source_hash, "segments": entries, "updated_at": int(time.time())}


def translate_document(doc: SourceDoc, entries: dict, client: TranslationClient) -> tuple[list[Segment], dict]:
    text = doc.text
    if doc.source == README_SOURCE:
        # Source discovery validates the boundary. Keep the body context independent of manual layout.
        text = "# ElBot\n\n" + text.split("<!-- manual-header:end -->", 1)[1].lstrip("\r\n")
    sections = [Segment(doc.source_rel, text, context) for text, context in split_sections(text)]
    if doc.source == README_SOURCE:
        sections = sections[1:]  # The synthetic title supplies context only; it is never published.
    pending = {section.key: section for section in sections
               if should_translate_text(section.text) and section.key not in entries}
    print(f"{doc.source_rel}: {len(entries)} cached, {len(pending)} pending", flush=True)
    with ThreadPoolExecutor(max_workers=WORKERS) as executor:
        futures = {executor.submit(client.translate_section, section): section for section in pending.values()}
        for future in as_completed(futures):
            section = futures[future]
            try:
                entries[section.key] = cache_entry(section, future.result())
            except RuntimeError as exc:
                print(f"warning: {doc.source_rel}: {exc}", flush=True)
    return sections, entries


def run_git(args: list[str]) -> str:
    result = subprocess.run(["git", *args], cwd=ROOT, text=True, capture_output=True,
                            env={**os.environ, "LC_ALL": "C"})
    if result.returncode:
        print(result.stderr, end="", file=sys.stderr)
    result.check_returncode()
    return result.stdout.strip()


def sync_upstream(base: str) -> str:
    run_git(["fetch"])
    upstream = run_git(["rev-parse", "@{upstream}"])
    if upstream != base:
        run_git(["merge", "--ff-only", "@{upstream}"])
    return upstream


def prune_deleted_sources(caches: dict, sources: dict) -> list[Path]:
    paths = []
    for source in caches.keys() - sources.keys():
        target = ROOT / caches[source]["target"]
        if target.exists():
            target.unlink()
            paths.append(target)
        path = cache_path_for_source(source)
        path.unlink()
        paths.append(path)
    return paths


def commit_translation(paths: list[Path], message: str) -> bool:
    if not paths:
        return True
    run_git(["add", "--", *(str(path.relative_to(ROOT)) for path in paths)])
    if not run_git(["diff", "--cached", "--name-only"]):
        return True
    run_git(["commit", "-m", message])
    try:
        run_git(["push", "--porcelain"])
    except subprocess.CalledProcessError as exc:
        if re.search(r"^!\t[^\n]*\t\[rejected\] \((?:fetch first|non-fast-forward)\)$", exc.stdout, re.MULTILINE):
            return False
        raise
    return True


def rewrite_readme_links(rendered: str) -> str:
    roman = {1: "I", 2: "II", 3: "III", 4: "IV", 5: "V", 6: "VI", 7: "VII", 8: "VIII", 9: "IX", 10: "X"}
    lines, in_features, fence, in_comment = rendered.splitlines(keepends=True), False, "", False
    parts, prose = [], []
    for index, line in enumerate(lines):
        marker = CODE_FENCE_RE.match(line)
        if marker and (not fence or (marker[1][0] == fence[0] and len(marker[1]) >= len(fence) and not marker[2].strip())):
            parts.append((False, "".join(prose)))
            prose = []
            parts.append((True, line))
            fence = "" if fence else marker[1]
            continue
        if fence:
            parts.append((True, line))
            continue
        if in_comment or "<!--" in line:
            in_comment = "-->" not in line
            prose.append(line)
            continue
        if line.startswith("## "):
            in_features = line.strip().lower() == "## features"
        if in_features:
            match = re.match(r"^(###\s+)(\d{1,2})([.)]\s+)(.*)$", line)
            if match and int(match[2]) in roman:
                lines[index] = f"{match[1]}{roman[int(match[2])]}. {match[4]}" + ("\n" if line.endswith("\n") else "")
        prose.append(lines[index])
    parts.append((False, "".join(prose)))
    # Process prose across line boundaries so multiline code spans remain literal.
    return "".join(text if literal else re.sub(
            r"(?P<code>(`+).*?\2|<!--.*?-->)|(?P<prefix>\]\(<?|\bhref\s*=\s*[\"'])docs/"
            r"(?P<path>[^\s<>\"')]+\.md(?:#[^\s<>\"')]+)?)(?=[>\"')\s])",
            lambda match: match[0] if match["code"] else match["prefix"] + "docs.en/" + match["path"],
            text, flags=re.DOTALL) for literal, text in parts)


def write_text_file(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def run(client: TranslationClient) -> None:
    def refresh():
        docs = {doc.source_rel: doc for doc in all_source_docs()}
        caches = {}
        for path in CACHE_DIR.rglob("*.json"):
            cache = json.loads(path.read_text(encoding="utf-8"))
            if cache["version"] != 3:
                raise ValueError(f"unsupported translation cache version: {path}")
            caches[cache["source"]] = cache
        return docs, caches

    docs, caches = refresh()
    base = run_git(["rev-parse", "HEAD"])
    attempted_versions, memory = {}, {}
    active, push_attempts = None, 0
    while True:
        if active is None and not caches.keys() - docs.keys():
            active = next((name for name, doc in docs.items()
                           if (name, doc.source_hash) not in attempted_versions
                           and (caches.get(name, {}).get("source_hash") != doc.source_hash or not doc.target.exists())), None)
            if active is None:
                break
        if active is not None:
            doc = docs[active]
            cache = caches.get(active, empty_doc_cache(doc))
            entries = {**memory.get(active, {}), **cache["segments"]}
            version = (active, doc.source_hash)
            if version not in attempted_versions:
                sections, entries = translate_document(doc, entries, client)
                attempted_versions[version] = sections
            else:
                sections = attempted_versions[version]
            memory[active] = entries

        upstream = sync_upstream(base)  # First fetch happens after document work, never at startup.
        if upstream != base:
            base = upstream
            docs, caches = refresh()
            if active is not None:
                if active not in docs:
                    active = None
                elif docs[active].source_hash != doc.source_hash:
                    continue

        if active is None:
            paths = prune_deleted_sources(caches, docs)
            message = "docs: remove translated deleted sources"
        else:
            cache = caches.get(active, empty_doc_cache(doc))
            entries = {**memory[active], **cache["segments"]}
            entries = {section.key: entries[section.key] for section in sections if section.key in entries}
            complete = all(section.key in entries or not should_translate_text(section.text) for section in sections)
            paths = [cache_path_for_source(active)]
            cache = make_doc_cache(doc, cache, entries, complete)
            if complete:
                rendered = "".join(
                    entries[section.key]["translation"] if section.key in entries else section.text for section in sections)
                if doc.target == README_TARGET:
                    # Read after syncing upstream, so concurrent manual edits survive publication.
                    header, _ = split_manual_header(doc.target.read_text(encoding="utf-8"))
                    rendered = header + rewrite_readme_links(rendered)
                else:
                    rendered = AUTO_HEADER_TEMPLATE.format(source=active) + rendered
                write_text_file(doc.target, rendered)
                paths.append(doc.target)
            write_text_file(paths[0], json.dumps(cache, ensure_ascii=False, indent=2, sort_keys=True) + "\n")
            message = f"docs: {'auto-translate' if complete else 'cache partial translation for'} {active}"

        if commit_translation(paths, message):
            if active is None:
                caches = {name: cache for name, cache in caches.items() if name in docs}
            else:
                caches[active] = cache
            base = run_git(["rev-parse", "HEAD"])
            active, push_attempts = None, 0
        else:
            push_attempts += 1
            if push_attempts == 3:
                raise RuntimeError("remote kept advancing; final generated commit retained")
            run_git(["reset", "--hard", base])


def main() -> None:
    run(TranslationClient())


if __name__ == "__main__":
    main()
