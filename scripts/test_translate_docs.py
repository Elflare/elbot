import io
import json
import os
import subprocess
import tempfile
import threading
import unittest
import urllib.error
from pathlib import Path
from unittest import mock

import translate_docs as td


class TranslationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        paths = {
            "ROOT": self.root, "SOURCE_DIR": self.root / "docs", "TARGET_DIR": self.root / "docs.en",
            "README_SOURCE": self.root / "README.zh-CN.md", "README_TARGET": self.root / "README.md",
            "CHANGELOG_SOURCE": self.root / "CHANGELOG.md", "CHANGELOG_TARGET": self.root / "CHANGELOG.en.md",
            "CACHE_DIR": self.root / ".translation-cache",
        }
        for name, path in paths.items():
            patch = mock.patch.object(td, name, path)
            patch.start()
            self.addCleanup(patch.stop)
        td.SOURCE_DIR.mkdir()
        td.TARGET_DIR.mkdir()
        self.client = mock.Mock()
        self.client.translate_section.side_effect = lambda section: section.text.replace("中文", "English")
        patch = mock.patch.dict(os.environ, {"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull})
        patch.start()
        self.addCleanup(patch.stop)

    def doc(self, name="test", text="## 标题\n\n中文内容。\n"):
        source = td.SOURCE_DIR / (name + ".md")
        source.write_text(text, encoding="utf-8")
        return td.SourceDoc(source, td.TARGET_DIR / source.name, text)

    def read_cache(self, doc):
        return json.loads(td.cache_path_for_source(doc.source_rel).read_text())

    def write_cache(self, root, doc, entries, complete=False):
        cache = td.make_doc_cache(doc, td.empty_doc_cache(doc), entries, complete)
        path = root / td.cache_path_for_source(doc.source_rel).relative_to(self.root)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(cache, ensure_ascii=False), encoding="utf-8")
        return cache

    def entries(self, doc, prefix="English"):
        return {section.key: td.cache_entry(section, text.replace("中文", prefix))
                for text, context in td.split_sections(doc.text)
                for section in [td.Segment(doc.source_rel, text, context)]}

    def job(self, workers=1):
        with mock.patch.object(td, "WORKERS", workers), mock.patch("sys.stdout", new_callable=io.StringIO), mock.patch("sys.stderr", new_callable=io.StringIO):
            td.run(self.client)

    def git(self, cwd, *args):
        return subprocess.run(["git", *args], cwd=cwd, check=True, text=True, capture_output=True).stdout.strip()

    def remote(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        remote, peer = Path(temp.name) / "remote.git", Path(temp.name) / "peer"
        self.git(self.root, "init", "--bare", "--initial-branch=main", str(remote))
        self.git(self.root, "init", "--initial-branch=main")
        self.git(self.root, "config", "user.name", "Translation Test")
        self.git(self.root, "config", "user.email", "translation@example.invalid")
        self.git(self.root, "add", ".")
        self.git(self.root, "commit", "-m", "initial")
        self.git(self.root, "remote", "add", "origin", str(remote))
        self.git(self.root, "push", "-u", "origin", "main")
        self.git(self.root, "clone", str(remote), str(peer))
        self.git(peer, "config", "user.name", "Other Writer")
        self.git(peer, "config", "user.email", "writer@example.invalid")
        return peer

    def push(self, peer):
        self.git(peer, "add", ".")
        self.git(peer, "commit", "-m", "remote advance")
        self.git(peer, "push")

    def test_sections_preserve_source_and_parent_hierarchy(self):
        text = "开头\n# 文档\n## 甲\n引言\n### 相同\n正文。另一句。\n#### 细节\n| 表 |\n````md\n### 代码\n```\n````\n<!--\n### 注释\n-->\n## 乙\n### 相同\n正文。另一句。\n"
        sections = td.split_sections(text)
        self.assertEqual("".join(raw for raw, _ in sections), text)
        self.assertEqual(len(sections), 6)
        self.assertEqual(sections[3][1], "文档 > 甲")
        self.assertIn("### 代码", sections[3][0])
        self.assertIn("#### 细节", sections[3][0])
        self.assertEqual(sections[-1][1], "文档 > 乙")
        self.assertEqual(td.split_sections("没有标题。\n\n整篇内容。"), [("没有标题。\n\n整篇内容。", "")])

    def test_validator_preserves_code_and_allows_only_documentation_comments(self):
        source = (
            "## 中文标题\n[中文说明](https://example.invalid/a) 和 `中文代码`。\n<!-- locator -->\n"
            "```yaml\nvalue: '中文值 # 字面量' # 中文注释 `key`\nother: true # English comment\n````\n"
            "```python\nprint('中文值') # 中文不翻译\n```\n"
        )
        good = source.replace("中文标题", "Title").replace("中文说明", "Description").replace("中文注释", "Documentation")
        td.validate_translation(source, good)
        for bad in ["", good.replace("example.invalid/a", "example.invalid/b"), good.replace("`中文代码`", "`code`"),
                    good.replace("value:", "wrong:"), good.replace("English comment", "changed comment"),
                    good.replace("中文不翻译", "translated"), good.replace("locator", "changed"), "```markdown\n" + good + "```\n"]:
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                td.validate_translation(source, bad)

    def test_api_returns_plain_markdown_and_validates_once(self):
        section = td.Segment("docs/test.md", "## 中文\n运行 `elbot`。\n\n", "文档")
        translated = section.text.replace("中文", "Title").replace("运行", "Run")
        response = mock.MagicMock()
        response.__enter__.return_value.read.return_value = json.dumps({"choices": [{"message": {"content": translated}}]}).encode()
        with (
            mock.patch.dict(os.environ, {"TRANSLATE_LLM_API_KEY": "test", "TRANSLATE_LLM_BASE_URL": "https://example.invalid", "TRANSLATE_LLM_MODEL": "model"}),
            mock.patch.object(td.urllib.request, "urlopen", return_value=response) as request,
            mock.patch.object(td, "validate_translation", wraps=td.validate_translation) as validate,
        ):
            self.assertEqual(td.TranslationClient().translate_section(section), translated)
        validate.assert_called_once_with(section.text, translated)
        payload = json.loads(request.call_args.args[0].data)
        self.assertEqual(payload["messages"][-1]["content"].count(section.text), 1)
        self.assertNotIn("__ELBOT_CODE_", payload["messages"][-1]["content"])
        self.assertEqual(request.call_args.kwargs["timeout"], 240)

    def test_bad_translation_retries_but_permissions_do_not(self):
        section = td.Segment("docs/test.md", "## 中文\n`elbot`\n", "")
        response = mock.MagicMock()
        response.__enter__.return_value.read.side_effect = [json.dumps({"choices": [{"message": {"content": content}}]}).encode()
                                                          for content in ["lost code", section.text]]
        with (
            mock.patch.dict(os.environ, {"TRANSLATE_LLM_API_KEY": "test", "TRANSLATE_LLM_BASE_URL": "https://example.invalid", "TRANSLATE_LLM_MODEL": "model"}),
            mock.patch.object(td.urllib.request, "urlopen", return_value=response) as request,
            mock.patch.object(td.time, "sleep"), mock.patch("sys.stdout", new_callable=io.StringIO),
        ):
            client = td.TranslationClient()
            self.assertEqual(client.translate_section(section), section.text)
            self.assertEqual(request.call_count, 2)
            request.reset_mock()
            request.side_effect = urllib.error.HTTPError("https://example.invalid", 401, "unauthorized", {}, None)
            with self.assertRaises(urllib.error.HTTPError):
                client.translate_section(section)
            request.assert_called_once()

    def test_two_requests_run_concurrently(self):
        self.assertEqual((td.WORKERS, td.RETRIES, td.REQUEST_TIMEOUT), (2, 5, 240))
        self.doc(text="## 一\n中文一。\n## 二\n中文二。\n")
        self.remote()
        barrier = threading.Barrier(2)

        def translate(section):
            barrier.wait(timeout=5)
            return section.text.replace("中文", "English")

        self.client.translate_section.side_effect = translate
        self.job(workers=2)
        self.assertEqual(self.client.translate_section.call_count, 2)

    def test_startup_has_no_fetch_and_source_is_read_once_without_remote_change(self):
        doc = self.doc()
        self.remote()
        events, reads = [], []
        original_git, original_read = td.run_git, Path.read_text

        def git(args):
            events.append(args[0])
            return original_git(args)

        def read(path, *args, **kwargs):
            if path == doc.source:
                reads.append(path)
            return original_read(path, *args, **kwargs)

        def translate(section):
            events.append("translate")
            return section.text.replace("中文", "English")

        self.client.translate_section.side_effect = translate
        with mock.patch.object(td, "run_git", side_effect=git), mock.patch.object(Path, "read_text", read):
            self.job()
        self.assertLess(events.index("translate"), events.index("fetch"))
        self.assertEqual(events.count("fetch"), 1)
        self.assertNotIn("status", events)
        self.assertNotIn("rev-list", events)
        self.assertEqual(len(reads), 1)

    def test_complete_v3_cache_is_a_noop_without_fetch_or_validation(self):
        doc = self.doc()
        self.write_cache(self.root, doc, self.entries(doc), complete=True)
        doc.target.write_text("existing target")
        self.remote()
        with mock.patch.object(td, "run_git", wraps=td.run_git) as git, mock.patch.object(td, "validate_translation") as validate:
            self.job()
        self.assertFalse(any(call.args[0][0] == "fetch" for call in git.call_args_list))
        self.client.translate_section.assert_not_called()
        validate.assert_not_called()
        self.assertEqual(doc.target.read_text(), "existing target")

    def test_partial_failure_saves_successes_and_does_not_repeat_the_version(self):
        doc = self.doc(text="## 成功\n中文成功。\n## 失败\n中文失败。\n")
        doc.target.write_text("keep target")
        self.doc("later")
        self.remote()

        def translate(section):
            if "失败" in section.text:
                raise RuntimeError("failed")
            return section.text.replace("中文", "English")

        self.client.translate_section.side_effect = translate
        self.job()
        self.assertEqual(self.client.translate_section.call_count, 3)
        self.assertEqual(doc.target.read_text(), "keep target")
        self.assertEqual(self.read_cache(doc)["source_hash"], "")
        self.assertEqual(len(self.read_cache(doc)["segments"]), 1)
        self.client.reset_mock()
        self.client.translate_section.side_effect = lambda section: section.text.replace("中文", "English")
        self.job()
        self.client.translate_section.assert_called_once()
        self.assertEqual(self.read_cache(doc)["source_hash"], doc.source_hash)

    def test_remote_unrelated_push_does_not_retranslate(self):
        doc = self.doc()
        peer = self.remote()

        def translate(section):
            (peer / "remote.txt").write_text("keep")
            self.push(peer)
            return section.text.replace("中文", "English")

        self.client.translate_section.side_effect = translate
        self.job()
        self.client.translate_section.assert_called_once()
        self.assertEqual((self.root / "remote.txt").read_text(), "keep")
        self.assertIn("English", doc.target.read_text())

    def test_changed_source_reuses_unchanged_sections(self):
        doc = self.doc(text="## 保留\n中文稳定。\n## 修改\n中文旧版。\n")
        peer = self.remote()
        seen = []

        def translate(section):
            seen.append(section.text)
            if len(seen) == 1:
                (peer / doc.source_rel).write_text(doc.text.replace("旧版", "新版"), encoding="utf-8")
                self.push(peer)
            return section.text.replace("中文", "English")

        self.client.translate_section.side_effect = translate
        self.job()
        self.assertEqual(len(seen), 3)
        self.assertEqual(sum("稳定" in text for text in seen), 1)
        self.assertNotIn("旧版", doc.target.read_text())
        self.assertIn("新版", doc.target.read_text())
        self.assertEqual(self.read_cache(doc)["source_hash"], td.sha256_text(doc.source.read_text()))

    def test_remote_cache_wins_and_fills_locally_failed_section(self):
        doc = self.doc(text="## 甲\n中文甲。\n## 乙\n中文乙。\n")
        peer = self.remote()

        def translate(section):
            if "乙" in section.text:
                raise RuntimeError("failed")
            self.write_cache(peer, doc, self.entries(doc, "REMOTE"))
            self.push(peer)
            return section.text.replace("中文", "LOCAL")

        self.client.translate_section.side_effect = translate
        self.job()
        self.assertEqual(self.client.translate_section.call_count, 2)
        self.assertIn("REMOTE甲", doc.target.read_text())
        self.assertIn("REMOTE乙", doc.target.read_text())
        self.assertNotIn("LOCAL", doc.target.read_text())

    def test_source_returning_to_an_attempted_version_reuses_its_results(self):
        doc = self.doc(text="## 内容\n中文旧版。\n")
        peer = self.remote()
        seen = []

        def translate(section):
            seen.append(section.text)
            text = doc.text.replace("旧版", "新版") if len(seen) == 1 else doc.text
            (peer / doc.source_rel).write_text(text, encoding="utf-8")
            self.push(peer)
            return section.text.replace("中文", "English")

        self.client.translate_section.side_effect = translate
        self.job()
        self.assertEqual(len(seen), 2)
        self.assertIn("旧版", doc.target.read_text())
        self.assertNotIn("新版", doc.target.read_text())
        self.assertEqual(self.read_cache(doc)["source_hash"], doc.source_hash)

    def test_push_race_rechecks_changed_source(self):
        doc = self.doc(text="## 内容\n中文旧版。\n")
        peer = self.remote()
        original_git, pushes = td.run_git, 0

        def git(args):
            nonlocal pushes
            if args[0] == "push":
                pushes += 1
                if pushes == 1:
                    (peer / doc.source_rel).write_text(doc.text.replace("旧版", "新版"), encoding="utf-8")
                    self.push(peer)
            return original_git(args)

        with mock.patch.object(td, "run_git", side_effect=git):
            self.job()
        self.assertEqual(pushes, 2)
        self.assertEqual(self.client.translate_section.call_count, 2)
        self.assertNotIn("旧版", doc.target.read_text())
        self.assertIn("新版", doc.target.read_text())

    def test_push_cache_collision_reuses_translation(self):
        doc = self.doc()
        peer = self.remote()
        original_git, pushes = td.run_git, 0

        def git(args):
            nonlocal pushes
            if args[0] == "push":
                pushes += 1
                if pushes == 1:
                    self.write_cache(peer, doc, self.entries(doc, "REMOTE"))
                    self.push(peer)
            return original_git(args)

        with mock.patch.object(td, "run_git", side_effect=git):
            self.job()
        self.assertEqual(pushes, 2)
        self.client.translate_section.assert_called_once()
        self.assertIn("REMOTE", doc.target.read_text())

    def test_dynamic_queue_updates_completed_docs_and_handles_additions_and_deletions(self):
        a = self.doc("a", "## A\n中文旧版。\n")
        b = self.doc("b")
        self.write_cache(self.root, b, {})  # Partial cache without a generated target is valid.
        peer = self.remote()

        def translate(section):
            if section.source_file == b.source_rel:
                self.git(peer, "pull", "--ff-only")
                (peer / a.source_rel).write_text(a.text.replace("旧版", "新版"), encoding="utf-8")
                (peer / "docs/c.md").write_text("## C\n中文新增。\n", encoding="utf-8")
                (peer / b.source_rel).unlink()
                self.push(peer)
            return section.text.replace("中文", "English")

        self.client.translate_section.side_effect = translate
        self.job()
        self.assertEqual(self.client.translate_section.call_count, 4)
        self.assertIn("新版", a.target.read_text())
        self.assertTrue((td.TARGET_DIR / "c.md").exists())
        self.assertFalse(td.cache_path_for_source(b.source_rel).exists())

    def test_repeated_push_races_keep_last_generated_commit(self):
        self.doc()
        peer = self.remote()
        original_git, pushes = td.run_git, 0

        def git(args):
            nonlocal pushes
            if args[0] == "push":
                pushes += 1
                (peer / f"remote-{pushes}.txt").write_text("remote")
                self.push(peer)
            return original_git(args)

        with mock.patch.object(td, "run_git", side_effect=git), self.assertRaisesRegex(RuntimeError, "final generated commit retained"):
            self.job()
        self.assertEqual(pushes, 3)
        self.client.translate_section.assert_called_once()
        self.assertIn("auto-translate", self.git(self.root, "log", "-1", "--format=%s"))

    def test_cache_corruption_fails_instead_of_retranslating(self):
        doc = self.doc()
        path = td.cache_path_for_source(doc.source_rel)
        path.parent.mkdir(parents=True)
        path.write_text("invalid JSON")
        with self.assertRaises(json.JSONDecodeError):
            self.job()
        self.client.translate_section.assert_not_called()

    def test_old_cache_version_is_rejected_without_an_adapter(self):
        doc = self.doc()
        path = td.cache_path_for_source(doc.source_rel)
        path.parent.mkdir(parents=True)
        path.write_text(json.dumps({**td.empty_doc_cache(doc), "version": 2}))
        with self.assertRaisesRegex(ValueError, "unsupported translation cache version"):
            self.job()
        self.client.translate_section.assert_not_called()

    def test_readme_keeps_switch_links_and_roman_numbering(self):
        text = "# ElBot\n\nChinese | [English](README.md)\n\n## Features\n### 1. Small\n### II. Fast\n## Usage\n[Docs](docs/concepts.md#skill)\n"
        result = td.rewrite_readme_links(text)
        self.assertIn("[中文](README.zh-CN.md) | English", result)
        self.assertIn("### I. Small", result)
        self.assertIn("### II. Fast", result)
        self.assertIn("(docs.en/concepts.md#skill)", result)


if __name__ == "__main__":
    unittest.main()
