import io
import json
import os
import subprocess
import tempfile
import threading
import unittest
from pathlib import Path
from unittest import mock

import translate_docs as td


class TranslationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        paths = {
            "ROOT": self.root, "SOURCE_DIR": self.root / "docs",
            "TARGET_DIR": self.root / "docs.en",
            "README_SOURCE": self.root / "README.zh-CN.md", "README_TARGET": self.root / "README.md",
            "CHANGELOG_SOURCE": self.root / "CHANGELOG.md", "CHANGELOG_TARGET": self.root / "CHANGELOG.en.md",
            "CACHE_DIR": self.root / ".translation-cache",
        }
        for name, value in paths.items():
            patch = mock.patch.object(td, name, value)
            patch.start()
            self.addCleanup(patch.stop)
        td.SOURCE_DIR.mkdir()
        td.TARGET_DIR.mkdir()
        self.client = mock.Mock()
        self.client.translate_batch.side_effect = self.translations
        patch = mock.patch.dict(os.environ, {"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull})
        patch.start()
        self.addCleanup(patch.stop)

    def doc(self, name="test", text="## 标题\n\n中文内容。\n"):
        source = td.SOURCE_DIR / f"{name}.md"
        source.write_text(text, encoding="utf-8")
        return td.SourceDoc(source, td.TARGET_DIR / source.name)

    def translations(self, batch, label):
        return {item.segment.key: td.mask_markdown(item.segment.text)[0].replace("中文", "English") for item in batch}

    def run_script(self, *argv):
        with (
            mock.patch.object(td, "make_client", return_value=self.client),
            mock.patch.object(td, "log"),
            mock.patch.object(td.sys, "stderr", new_callable=io.StringIO) as errors,
        ):
            result = td.main(["--batch-delay", "0", "--parallel-batches", "1", *argv])
        self.errors = errors.getvalue()
        return result

    def git(self, cwd, *args):
        return subprocess.run(["git", *args], cwd=cwd, check=True, text=True, capture_output=True)

    def remote(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        remote = Path(temp.name) / "remote.git"
        peer = Path(temp.name) / "peer"
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

    def push_peer(self, peer, message="remote advance"):
        self.git(peer, "add", ".")
        self.git(peer, "commit", "-m", message)
        self.git(peer, "push")

    def test_sections_are_lossless_and_keep_markdown_blocks(self):
        text = (
            "开头\n# 文档\n\n## 一级小节\n引言\n\n### 子节\n"
            "连续的两句。另一句。\n换行仍在同一节。\n\n"
            "#### 详细说明\n- 列表\n\n| 表头 | 值 |\n| --- | --- |\n| 中文 | 内容 |\n"
            "````md\n### 代码里的标题\n```\n````\n"
            "<!--\n### 注释里的标题\n-->\n### 下节\n结尾\n## 第二章\n说明\n"
        )
        sections = td.split_sections(text)
        self.assertEqual("".join(raw for raw, _ in sections), text)
        self.assertEqual(len(sections), 6)
        self.assertEqual(sections[3][1], "文档 > 一级小节")
        self.assertIn("#### 详细说明", sections[3][0])
        self.assertIn("### 代码里的标题", sections[3][0])
        self.assertEqual(sections[-1][1], "文档")
        self.assertEqual(td.split_sections("没有标题。\n\n完整文档。"), [("没有标题。\n\n完整文档。", "")])

    def test_parent_context_does_not_include_sibling_headings(self):
        sections = td.split_sections("# 文档\n## 甲\n### 相同\n内容\n## 乙\n### 相同\n内容\n")
        self.assertEqual(sections[2][1], "文档 > 甲")
        self.assertEqual(sections[4][1], "文档 > 乙")
        a = td.Segment("docs/test.md", 2, "section", sections[2][0], sections[2][1])
        b = td.Segment("docs/test.md", 4, "section", sections[4][0], sections[4][1])
        self.assertNotEqual(a.key, b.key)

    def test_cache_reuses_sections_after_insert_and_drops_obsolete_entries(self):
        doc = self.doc(text="## 第一节\n中文一。\n\n## 第二节\n中文二。\n")
        self.assertEqual(self.run_script(), 0)
        old = td.read_doc_cache(doc)
        self.client.reset_mock()
        doc.source.write_text("## 新节\n中文新增。\n\n" + doc.source.read_text(encoding="utf-8"), encoding="utf-8")
        old["segments"]["obsolete"] = {"translation": "unused"}
        td.write_json(td.cache_path_for_source(doc.source_rel), old)
        self.assertEqual(self.run_script(), 0)
        self.client.translate_batch.assert_called_once()
        self.assertEqual(len(td.read_doc_cache(doc)["segments"]), 3)
        self.assertNotIn("obsolete", td.read_doc_cache(doc)["segments"])
        self.assertEqual(td.read_doc_cache(doc)["version"], 3)

    def test_code_destinations_and_spacing_survive_section_translation(self):
        text = (
            "## 中文\n\n[中文链接](https://example.invalid/a) 和 `中文原样`。\n"
            "```yaml\nvalue: '中文值 # 不是注释' # 中文注释\n````\n"
            "```python\nprint('中文原样')  # 中文原样\n```\n\n"
        )
        doc = self.doc(text=text)
        result = td.MarkdownRenderer({"segments": {}}).render_file(doc.source)
        item = result.pending[0]
        payload = td.TranslationClient.batch_payload([item])
        self.assertEqual(payload["items"][0]["source_markdown"], text)
        masked = payload["items"][0]["text"]
        self.assertNotIn("中文值", masked)
        self.assertNotIn("中文原样", masked)
        self.assertIn("中文注释", masked)
        response = json.dumps([{"key": item.segment.key, "translation": masked.replace("中文", "English")}])
        translated = td.TranslationClient.parse_batch_response(response, [item])[item.segment.key]
        restored = td.unmask_inline_code(translated, item.masks)
        self.assertIn("value: '中文值 # 不是注释' # English注释", restored)
        self.assertIn("print('中文原样')  # 中文原样", restored)
        self.assertIn("[English链接](https://example.invalid/a)", restored)
        self.assertTrue(restored.endswith("\n\n"))
        broken = json.dumps([{"key": item.segment.key, "translation": masked.replace(next(iter(item.masks)), "")}])
        with self.assertRaisesRegex(ValueError, "protected Markdown token"):
            td.TranslationClient.parse_batch_response(broken, [item])

    def test_snapshot_hash_is_not_replaced_by_a_later_read(self):
        doc = self.doc()
        rendered = td.MarkdownRenderer({}).render_file(doc.source)
        doc.source.write_text("新的原文。", encoding="utf-8")
        cache = td.make_doc_cache(doc, {}, {}, rendered.source_hash)
        self.assertEqual(cache["source_hash"], rendered.source_hash)
        self.assertNotEqual(cache["source_hash"], td.sha256_text(doc.source.read_text(encoding="utf-8")))

    def test_invalid_protected_token_response_is_retried(self):
        doc = self.doc(text="## 中文\n\n运行 `elbot`。\n")
        item = td.MarkdownRenderer({}).render_file(doc.source).pending[0]
        good = json.dumps([{"key": item.segment.key, "translation": td.mask_markdown(item.segment.text)[0]}])
        bad = json.dumps([{"key": item.segment.key, "translation": "missing protected code"}])
        responses = [json.dumps({"choices": [{"message": {"content": text}}]}).encode() for text in [bad, good]]
        response = mock.MagicMock()
        response.__enter__.return_value.read.side_effect = responses
        client = td.TranslationClient("key", "https://example.invalid", "model", retries=1)
        with (
            mock.patch.object(td.urllib.request, "urlopen", return_value=response) as request,
            mock.patch.object(td.time, "sleep"),
            mock.patch.object(td, "log"),
        ):
            self.assertIn(item.segment.key, client.translate_batch([item]))
        self.assertEqual(request.call_count, 2)

    def test_default_is_one_section_and_two_concurrent_requests(self):
        args = td.parse_args([])
        self.assertEqual((args.batch_size, args.parallel_batches), (1, 2))
        self.doc(text="## 一\n中文一。\n## 二\n中文二。\n")
        barrier = threading.Barrier(2)

        def translate(batch, label):
            self.assertEqual(len(batch), 1)
            barrier.wait(timeout=5)
            return self.translations(batch, label)

        self.client.translate_batch.side_effect = translate
        self.assertEqual(self.run_script("--parallel-batches", "2"), 0)
        self.assertEqual(self.client.translate_batch.call_count, 2)

    def test_partial_sections_resume_without_repeating_successes(self):
        doc = self.doc(text="## 成功\n中文成功。\n## 失败\n中文失败。\n## 后续\n中文后续。\n")
        doc.target.write_text("old target", encoding="utf-8")

        def translate(batch, label):
            if "失败" in batch[0].segment.text:
                raise RuntimeError("exhausted retries")
            return self.translations(batch, label)

        self.client.translate_batch.side_effect = translate
        self.assertEqual(self.run_script(), 0)
        cache = td.read_doc_cache(doc)
        self.assertEqual(cache["source_hash"], "")
        self.assertEqual(len(cache["segments"]), 2)
        self.assertEqual(doc.target.read_text(), "old target")
        self.client.reset_mock()
        self.client.translate_batch.side_effect = self.translations
        self.assertEqual(self.run_script(), 0)
        self.client.translate_batch.assert_called_once()
        self.assertEqual(td.read_doc_cache(doc)["source_hash"], td.sha256_text(doc.source.read_text()))
        self.assertNotIn("ELBOT_SEGMENT_", doc.target.read_text())

    def test_unrelated_push_during_translation_does_not_retranslate(self):
        doc = self.doc()
        peer = self.remote()

        def translate(batch, label):
            (peer / "remote.txt").write_text("remote\n")
            self.push_peer(peer)
            return self.translations(batch, label)

        self.client.translate_batch.side_effect = translate
        with mock.patch.object(td, "run_git", wraps=td.run_git) as git:
            self.assertEqual(self.run_script("--commit-each"), 0, self.errors)
        self.client.translate_batch.assert_called_once()
        commands = [call.args[0] for call in git.call_args_list]
        self.assertFalse(any(command[0] in {"rebase", "reset"} for command in commands))
        self.assertEqual((self.root / "remote.txt").read_text(), "remote\n")
        self.assertIn("English", doc.target.read_text())
        self.assertEqual(self.git(self.root, "status", "--porcelain").stdout, "")

    def test_source_change_retranslates_only_changed_sections(self):
        old = "## 保留\n中文稳定。\n## 修改\n中文旧版。\n"
        new = old.replace("旧版", "新版")
        doc = self.doc(text=old)
        peer = self.remote()
        seen = []

        def translate(batch, label):
            seen.append(batch[0].segment.text)
            if len(seen) == 1:
                (peer / doc.source_rel).write_text(new, encoding="utf-8")
                self.push_peer(peer)
            return self.translations(batch, label)

        self.client.translate_batch.side_effect = translate
        self.assertEqual(self.run_script("--commit-each"), 0, self.errors)
        self.assertEqual(len(seen), 3)
        self.assertEqual(sum("稳定" in text for text in seen), 1)
        self.assertNotIn("旧版", doc.target.read_text())
        self.assertIn("新版", doc.target.read_text())
        self.assertEqual(td.read_doc_cache(doc)["source_hash"], td.sha256_text(new))

    def test_remote_cache_wins_and_completes_locally_failed_sections(self):
        doc = self.doc(text="## 甲\n中文甲。\n## 乙\n中文乙。\n")
        doc.target.write_text("old target")
        peer = self.remote()
        result = td.MarkdownRenderer({}).render_file(doc.source)
        remote_entries = {item.segment.key: td.cache_entry(item, item.segment.text.replace("中文", "REMOTE")) for item in result.pending}
        remote_cache = td.make_doc_cache(doc, {}, remote_entries, "")

        def translate(batch, label):
            if "乙" in batch[0].segment.text:
                raise RuntimeError("failed")
            cache_path = peer / td.cache_path_for_source(doc.source_rel).relative_to(self.root)
            cache_path.parent.mkdir(parents=True)
            cache_path.write_text(json.dumps(remote_cache, ensure_ascii=False), encoding="utf-8")
            self.push_peer(peer)
            return self.translations(batch, label)

        self.client.translate_batch.side_effect = translate
        self.assertEqual(self.run_script("--commit-each"), 0, self.errors)
        self.assertEqual(self.client.translate_batch.call_count, 2)
        self.assertIn("REMOTE甲", doc.target.read_text())
        self.assertIn("REMOTE乙", doc.target.read_text())
        self.assertNotIn("English", doc.target.read_text())

    def test_push_window_reconciles_cache_conflict_without_retranslation(self):
        doc = self.doc()
        peer = self.remote()
        original_git = td.run_git
        pushes = 0

        def git(args):
            nonlocal pushes
            if args[0] == "push":
                pushes += 1
                if pushes == 1:
                    result = td.MarkdownRenderer({}).render_file(doc.source)
                    entries = {item.segment.key: td.cache_entry(item, "## Remote\n\nREMOTE translation.\n") for item in result.pending}
                    cache_path = peer / td.cache_path_for_source(doc.source_rel).relative_to(self.root)
                    cache_path.parent.mkdir(parents=True)
                    cache_path.write_text(json.dumps(td.make_doc_cache(doc, {}, entries, "")))
                    self.push_peer(peer)
            return original_git(args)

        with mock.patch.object(td, "run_git", side_effect=git):
            self.assertEqual(self.run_script("--commit-each"), 0, self.errors)
        self.assertEqual(pushes, 2)
        self.client.translate_batch.assert_called_once()
        self.assertIn("REMOTE translation", doc.target.read_text())
        self.assertEqual(self.git(self.root, "status", "--porcelain").stdout, "")

    def test_dynamic_queue_revisits_changes_and_handles_additions_and_deletions(self):
        a = self.doc("a", "## A\n中文旧版。\n")
        b = self.doc("b", "## B\n中文删除。\n")
        b.target.write_text("old B")
        td.write_json(td.cache_path_for_source(b.source_rel), td.empty_doc_cache(b))
        peer = self.remote()
        seen = []

        def translate(batch, label):
            item = batch[0]
            seen.append(item.segment.text)
            if item.segment.source_file == b.source_rel:
                self.git(peer, "pull", "--ff-only")
                (peer / a.source_rel).write_text("## A\n中文新版。\n", encoding="utf-8")
                (peer / "docs/c.md").write_text("## C\n中文新增。\n", encoding="utf-8")
                (peer / b.source_rel).unlink()
                self.push_peer(peer)
            return self.translations(batch, label)

        self.client.translate_batch.side_effect = translate
        self.assertEqual(self.run_script("--commit-each"), 0, self.errors)
        self.assertEqual(len(seen), 4)
        self.assertIn("新版", a.target.read_text())
        self.assertTrue((td.TARGET_DIR / "c.md").exists())
        self.assertFalse(b.target.exists())
        self.assertFalse(td.cache_path_for_source(b.source_rel).exists())

    def test_source_change_in_push_window_rechecks_before_republishing(self):
        doc = self.doc(text="## 内容\n中文旧版。\n")
        peer = self.remote()
        original_git = td.run_git
        pushes = 0

        def git(args):
            nonlocal pushes
            if args[0] == "push":
                pushes += 1
                if pushes == 1:
                    (peer / doc.source_rel).write_text("## 内容\n中文新版。\n", encoding="utf-8")
                    self.push_peer(peer)
            return original_git(args)

        with mock.patch.object(td, "run_git", side_effect=git):
            self.assertEqual(self.run_script("--commit-each"), 0, self.errors)
        self.assertEqual(pushes, 2)
        self.assertEqual(self.client.translate_batch.call_count, 2)
        self.assertNotIn("旧版", doc.target.read_text())
        self.assertIn("新版", doc.target.read_text())
        self.assertEqual(td.read_doc_cache(doc)["source_hash"], td.sha256_text(doc.source.read_text()))

    def test_deleted_artifact_cleanup_retries_against_latest_remote(self):
        doc = self.doc()
        doc.target.write_text("old target")
        td.write_json(td.cache_path_for_source(doc.source_rel), td.empty_doc_cache(doc))
        doc.source.unlink()
        peer = self.remote()
        original_git = td.run_git
        pushes = 0

        def git(args):
            nonlocal pushes
            if args[0] == "push":
                pushes += 1
                if pushes == 1:
                    (peer / "unrelated.txt").write_text("keep")
                    self.push_peer(peer)
            return original_git(args)

        with mock.patch.object(td, "run_git", side_effect=git):
            self.assertEqual(self.run_script("--commit-each"), 0, self.errors)
        self.assertEqual(pushes, 2)
        self.assertFalse(doc.target.exists())
        self.assertFalse(td.cache_path_for_source(doc.source_rel).exists())
        self.assertEqual((self.root / "unrelated.txt").read_text(), "keep")
        self.client.translate_batch.assert_not_called()

    def test_failed_version_is_only_attempted_once_in_a_publishing_run(self):
        self.doc("a")
        self.doc("b")
        self.remote()
        self.client.translate_batch.side_effect = RuntimeError("failed")
        self.assertEqual(self.run_script("--commit-each"), 0, self.errors)
        self.assertEqual(self.client.translate_batch.call_count, 2)
        self.assertEqual(self.git(self.root, "status", "--porcelain").stdout, "")

    def test_repeated_push_races_are_bounded_and_retain_last_commit(self):
        self.doc()
        peer = self.remote()
        original_git = td.run_git
        pushes = 0

        def git(args):
            nonlocal pushes
            if args[0] == "push":
                pushes += 1
                (peer / f"remote-{pushes}.txt").write_text("remote\n")
                self.push_peer(peer)
            return original_git(args)

        with mock.patch.object(td, "run_git", side_effect=git):
            self.assertEqual(self.run_script("--commit-each"), 1)
        self.assertEqual(pushes, 3)
        self.assertIn("generated commit retained", self.errors)
        self.client.translate_batch.assert_called_once()
        self.assertIn("docs: auto-translate", self.git(self.root, "log", "-1", "--format=%s").stdout)

    def test_permission_failure_is_not_retried_or_reset(self):
        self.doc()
        self.remote()
        original_git = td.run_git

        def git(args):
            if args[0] == "push":
                raise subprocess.CalledProcessError(1, ["git", *args], output="", stderr="Permission denied")
            return original_git(args)

        with mock.patch.object(td, "run_git", side_effect=git) as calls:
            self.assertEqual(self.run_script("--commit-each"), 1)
        commands = [call.args[0] for call in calls.call_args_list]
        self.assertEqual(sum(command[0] == "push" for command in commands), 1)
        self.assertFalse(any(command[0] == "reset" for command in commands))

    def test_publishing_rejects_dirty_or_unpublished_user_work(self):
        doc = self.doc()
        self.remote()
        doc.source.write_text("用户改动\n", encoding="utf-8")
        self.assertEqual(self.run_script("--commit-each"), 1)
        self.assertIn("clean dedicated worktree", self.errors)
        self.assertEqual(doc.source.read_text(), "用户改动\n")
        self.git(self.root, "add", ".")
        self.git(self.root, "commit", "-m", "user unpublished")
        self.assertEqual(self.run_script("--commit-each"), 1)
        self.assertIn("without unpublished commits", self.errors)
        self.client.translate_batch.assert_not_called()

    def test_readme_numbering_and_stale_detection(self):
        rendered = td.rewrite_readme_feature_numbering("## Features\n\n### 1. Small\n\n### II. Fast\n\n## Usage\n")
        self.assertIn("### I. Small", rendered)
        doc = self.doc()
        args = td.parse_args(["--changed-only", "--base-ref", "base", "--head-ref", "head"])
        completed = subprocess.CompletedProcess([], 0, stdout="", stderr="")
        with mock.patch.object(td.subprocess, "run", return_value=completed):
            self.assertEqual(td.changed_source_names(args, [doc], {doc.source_rel: td.empty_doc_cache(doc)}), {doc.source_rel})


if __name__ == "__main__":
    unittest.main()
