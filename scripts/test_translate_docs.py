import argparse
import io
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import translate_docs as td


class TranslationCacheTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        names = (
            "ROOT",
            "SOURCE_DIR",
            "TARGET_DIR",
            "README_SOURCE",
            "README_TARGET",
            "CHANGELOG_SOURCE",
            "CHANGELOG_TARGET",
            "CACHE_DIR",
            "LEGACY_CACHE_PATH",
        )
        self.originals = {name: getattr(td, name) for name in names}
        td.ROOT = self.root
        td.SOURCE_DIR = self.root / "docs"
        td.TARGET_DIR = self.root / "docs.en"
        td.README_SOURCE = self.root / "README.zh-CN.md"
        td.README_TARGET = self.root / "README.md"
        td.CHANGELOG_SOURCE = self.root / "CHANGELOG.md"
        td.CHANGELOG_TARGET = self.root / "CHANGELOG.en.md"
        td.CACHE_DIR = self.root / ".translation-cache"
        td.LEGACY_CACHE_PATH = td.TARGET_DIR / ".translation-cache.json"
        td.SOURCE_DIR.mkdir(parents=True)
        td.TARGET_DIR.mkdir(parents=True)

    def tearDown(self) -> None:
        for name, value in self.originals.items():
            setattr(td, name, value)
        self.temp_dir.cleanup()

    def make_doc(self, rel: str, target_rel: str, content: str = "测试。\n") -> td.SourceDoc:
        source = self.root / Path(*rel.split("/"))
        target = self.root / Path(*target_rel.split("/"))
        source.parent.mkdir(parents=True, exist_ok=True)
        source.write_text(content, encoding="utf-8")
        return td.SourceDoc(source, target)

    def test_cache_path_mirrors_source_path(self) -> None:
        path = td.cache_path_for_source("docs/hooks.md")
        self.assertEqual(path, td.CACHE_DIR / "docs" / "hooks.md.json")
        self.assertEqual(td.source_for_cache_path(path), "docs/hooks.md")

    def test_migration_copies_entries_without_retranslation(self) -> None:
        doc = self.make_doc("docs/hooks.md", "docs.en/hooks.md")
        stale_target = self.root / "docs.en" / "old.md"
        stale_target.write_text("old", encoding="utf-8")
        entry = {
            "source_hash": "segment-hash",
            "source": "原文",
            "translation": "translation",
            "kind": "paragraph",
            "source_file": doc.source_rel,
            "updated_at": 1,
        }
        legacy = {
            "version": 1,
            "files": {
                doc.source_rel: {"source_hash": "document-hash", "updated_at": 2},
                "docs/old.md": {"source_hash": "old", "updated_at": 1},
            },
            "segments": {f"{doc.source_rel}:paragraph:segment-hash": entry},
        }
        td.write_json(td.LEGACY_CACHE_PATH, legacy)

        changed = td.migrate_legacy_cache([doc])

        cache_path = td.cache_path_for_source(doc.source_rel)
        migrated = json.loads(cache_path.read_text(encoding="utf-8"))
        self.assertEqual(migrated["source_hash"], "document-hash")
        self.assertEqual(list(migrated["segments"].values()), [entry])
        self.assertFalse(td.LEGACY_CACHE_PATH.exists())
        self.assertFalse(stale_target.exists())
        self.assertIn(cache_path, changed)

    def test_stale_hash_is_selected_even_when_git_diff_is_empty(self) -> None:
        doc = self.make_doc("docs/hooks.md", "docs.en/hooks.md")
        doc.target.write_text("translated", encoding="utf-8")
        cache = td.empty_doc_cache(doc)
        cache["source_hash"] = "outdated"
        args = argparse.Namespace(changed_only=True, base_ref="base", head_ref="head")
        completed = subprocess.CompletedProcess([], 0, stdout="", stderr="")

        with mock.patch.object(td.subprocess, "run", return_value=completed):
            changed = td.changed_source_names(args, [doc], {doc.source_rel: cache})

        self.assertEqual(changed, {doc.source_rel})

    def test_successful_render_drops_obsolete_segments(self) -> None:
        doc = self.make_doc("docs/hooks.md", "docs.en/hooks.md")
        first = td.MarkdownRenderer(cache={"segments": {}}).render_file(doc.source)
        td.translate_pending(
            first.text,
            first.pending,
            first.cache_entries,
            None,
            argparse.Namespace(dry_run=True),
            doc.source_rel,
        )
        cache = {"segments": dict(first.cache_entries)}
        cache["segments"]["obsolete"] = {
            "source_hash": "old",
            "translation": "old",
            "source_file": doc.source_rel,
        }

        second = td.MarkdownRenderer(cache=cache).render_file(doc.source)

        self.assertEqual(second.pending, [])
        self.assertNotIn("obsolete", second.cache_entries)
        self.assertEqual(len(second.cache_entries), len(first.cache_entries))

    def test_readme_feature_numbering_is_stable(self) -> None:
        rendered = "## Features\n\n### 1. Lightweight\n\n### II. Extensible\n\n## Quick Start\n"
        rewritten = td.rewrite_readme_feature_numbering(rendered)
        self.assertIn("### I. Lightweight", rewritten)
        self.assertIn("### II. Extensible", rewritten)

    def test_partial_translation_continues_and_resumes(self) -> None:
        for workers in (1, 2):
            for commit_each in (False, True):
                for existing_target in (False, True):
                    with self.subTest(workers=workers, commit_each=commit_each, existing_target=existing_target):
                        name = f"partial-{workers}-{commit_each}-{existing_target}"
                        doc = self.make_doc(f"docs/{name}.md", f"docs.en/{name}.md", "成功。\n\n失败。\n\n后续。\n")
                        later = self.make_doc(f"docs/{name}-later.md", f"docs.en/{name}-later.md", "其他。\n")
                        if existing_target:
                            doc.target.write_text("old translation", encoding="utf-8")
                        argv = ["--batch-size", "1", "--batch-delay", "0", "--parallel-batches", str(workers)]
                        if commit_each:
                            argv.append("--commit-each")
                        args = td.parse_args(argv)
                        client = mock.Mock()

                        def translate(batch, label):
                            if batch[0].segment.text.strip() == "失败。":
                                raise RuntimeError(f"{label} translation failed: exhausted retries")
                            return {item.segment.key: "Translated: " + item.segment.text for item in batch}

                        client.translate_batch.side_effect = translate
                        with (
                            mock.patch.object(td, "all_source_docs", return_value=[doc, later]),
                            mock.patch.object(td, "make_client", return_value=client),
                            mock.patch.object(td, "prune_deleted_sources", return_value=[]),
                            mock.patch.object(td, "commit_translation") as commit,
                            mock.patch.object(td, "log") as log,
                        ):
                            self.assertEqual(td.run(args), 0)

                        self.assertEqual(client.translate_batch.call_count, 4)
                        self.assertIn("Translated:", later.target.read_text(encoding="utf-8"))
                        if existing_target:
                            self.assertEqual(doc.target.read_text(encoding="utf-8"), "old translation")
                        else:
                            self.assertFalse(doc.target.exists())
                        cache_path = td.cache_path_for_source(doc.source_rel)
                        partial_cache = td.read_doc_cache(doc)
                        self.assertEqual(partial_cache["source_hash"], "")
                        self.assertEqual(len(partial_cache["segments"]), 2)
                        self.assertNotIn("失败。", {entry["source"].strip() for entry in partial_cache["segments"].values()})
                        self.assertTrue(any("warning:" in call.args[0] and doc.source_rel in call.args[0]
                                            and "batch 2/3" in call.args[0] for call in log.call_args_list))
                        self.assertTrue(any("incomplete=1" in call.args[0] for call in log.call_args_list))
                        if commit_each:
                            self.assertEqual(commit.call_count, 2)
                            self.assertEqual(commit.call_args_list[0].args[0], [cache_path])
                            self.assertEqual(commit.call_args_list[1].args[0], [later.target, td.cache_path_for_source(later.source_rel)])
                        else:
                            commit.assert_not_called()

                        args.changed_only = True
                        args.base_ref = "base"
                        args.head_ref = "head"
                        client.translate_batch.reset_mock()
                        client.translate_batch.side_effect = lambda batch, label: {
                            item.segment.key: "Recovered.\n" for item in batch
                        }
                        with (
                            mock.patch.object(td, "all_source_docs", return_value=[doc, later]),
                            mock.patch.object(td, "make_client", return_value=client),
                            mock.patch.object(td, "prune_deleted_sources", return_value=[]),
                            mock.patch.object(td, "commit_translation") as commit,
                            mock.patch.object(td.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, stdout="")),
                            mock.patch.object(td, "log"),
                        ):
                            self.assertEqual(td.run(args), 0)

                        client.translate_batch.assert_called_once()
                        self.assertEqual(client.translate_batch.call_args.args[0][0].segment.text.strip(), "失败。")
                        completed_cache = td.read_doc_cache(doc)
                        self.assertEqual(completed_cache["source_hash"], td.sha256_text(doc.source.read_text(encoding="utf-8")))
                        self.assertEqual(len(completed_cache["segments"]), 3)
                        for key, entry in partial_cache["segments"].items():
                            self.assertEqual(completed_cache["segments"][key], entry)
                        rendered = doc.target.read_text(encoding="utf-8")
                        self.assertIn("Recovered.", rendered)
                        self.assertIn("Translated: 成功。", rendered)
                        self.assertIn("Translated: 后续。", rendered)
                        self.assertNotIn("ELBOT_SEGMENT_", rendered)
                        if commit_each:
                            commit.assert_called_once_with([doc.target, cache_path], f"docs: auto-translate {doc.source_rel}")

    def test_exhausted_retries_leave_document_pending(self) -> None:
        doc = self.make_doc("docs/failure.md", "docs.en/failure.md")
        client = td.TranslationClient("test-key", "https://example.invalid", "test-model", retries=1)
        with (
            mock.patch.object(td, "make_client", return_value=client),
            mock.patch.object(td.urllib.request, "urlopen", side_effect=TimeoutError("timed out")) as request,
            mock.patch.object(td.time, "sleep"),
            mock.patch.object(td, "log"),
        ):
            self.assertEqual(td.main([]), 0)
        self.assertEqual(request.call_count, 2)
        self.assertFalse(doc.target.exists())
        cache = td.read_doc_cache(doc)
        self.assertEqual(cache["source_hash"], "")
        self.assertEqual(cache["segments"], {})

    def test_non_translation_errors_remain_fatal(self) -> None:
        self.make_doc("docs/fatal.md", "docs.en/fatal.md")
        client = mock.Mock()
        client.translate_batch.side_effect = lambda batch, label: {
            item.segment.key: "Translated.\n" for item in batch
        }
        for operation in ("make_client", "write_text_file", "write_json", "add", "commit", "push"):
            with self.subTest(operation=operation):
                def git(args):
                    if args[0] == operation:
                        raise subprocess.CalledProcessError(1, ["git", *args])
                    return subprocess.CompletedProcess(args, 0, stdout="")

                with (
                    mock.patch.object(td, "make_client", return_value=client) as make_client,
                    mock.patch.object(td, "write_text_file") as write_text,
                    mock.patch.object(td, "write_json") as write_json,
                    mock.patch.object(td, "run_git", side_effect=git),
                    mock.patch.object(td, "has_staged_changes", return_value=True),
                    mock.patch.object(td, "log"),
                    mock.patch.object(td.sys, "stderr"),
                ):
                    if operation == "make_client":
                        make_client.side_effect = ValueError("missing configuration")
                    elif operation == "write_text_file":
                        write_text.side_effect = OSError("write failed")
                    elif operation == "write_json":
                        write_json.side_effect = OSError("cache write failed")
                    self.assertEqual(td.main(["--commit-each"]), 1)

    def git(self, cwd: Path, *args: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(["git", *args], cwd=cwd, check=True, text=True, capture_output=True)

    def make_git_remote(self) -> Path:
        environment = mock.patch.dict(os.environ, {"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull})
        environment.start()
        self.addCleanup(environment.stop)
        remote_dir = tempfile.TemporaryDirectory()
        self.addCleanup(remote_dir.cleanup)
        remote = Path(remote_dir.name) / "remote.git"
        peer = Path(remote_dir.name) / "peer"
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

    def test_remote_advance_repushes_without_retranslation(self) -> None:
        doc = self.make_doc("docs/push.md", "docs.en/push.md")
        peer = self.make_git_remote()
        (peer / "remote.txt").write_text("remote change\n", encoding="utf-8")
        self.git(peer, "add", "remote.txt")
        self.git(peer, "commit", "-m", "remote advance")
        self.git(peer, "push")
        remote_head = self.git(peer, "rev-parse", "HEAD").stdout.strip()
        client = mock.Mock()
        client.translate_batch.side_effect = lambda batch, label: {
            item.segment.key: "Translated.\n" for item in batch
        }
        with (
            mock.patch.object(td, "make_client", return_value=client),
            mock.patch.object(td, "run_git", wraps=td.run_git) as git,
            mock.patch.object(td, "log"),
            mock.patch.object(td.sys, "stderr", new_callable=io.StringIO) as errors,
        ):
            self.assertEqual(td.main(["--commit-each"]), 0)

        client.translate_batch.assert_called_once()
        commands = [call.args[0] for call in git.call_args_list]
        self.assertEqual(commands.count(["push", "--porcelain"]), 2)
        self.assertEqual(sum(command[0] == "commit" for command in commands), 1)
        self.assertIn("[rejected] (fetch first)", errors.getvalue())
        self.git(peer, "pull", "--ff-only")
        self.git(peer, "merge-base", "--is-ancestor", remote_head, "HEAD")
        self.assertIn("Translated.", (peer / doc.target_rel).read_text(encoding="utf-8"))
        self.assertEqual((self.root / "remote.txt").read_text(encoding="utf-8"), "remote change\n")
        self.assertEqual(self.git(peer, "rev-list", "--count", f"{remote_head}..HEAD").stdout.strip(), "1")
        self.assertEqual(self.git(self.root, "status", "--porcelain").stdout, "")

    def test_rebase_conflict_aborts_and_preserves_translation_commit(self) -> None:
        doc = self.make_doc("docs/conflict.md", "docs.en/conflict.md")
        doc.target.write_text("old translation\n", encoding="utf-8")
        peer = self.make_git_remote()
        (peer / doc.target_rel).write_text("remote translation\n", encoding="utf-8")
        self.git(peer, "add", doc.target_rel)
        self.git(peer, "commit", "-m", "remote translation")
        self.git(peer, "push")
        doc.target.write_text("local translation\n", encoding="utf-8")
        with (
            mock.patch.object(td, "run_git", wraps=td.run_git) as git,
            mock.patch.object(td, "log"),
            mock.patch.object(td.sys, "stderr", new_callable=io.StringIO) as errors,
        ):
            with self.assertRaises(subprocess.CalledProcessError) as failure:
                td.commit_translation([doc.target], "local translation")

        self.assertEqual(failure.exception.cmd, ["git", "rebase", "@{upstream}"])
        self.assertIn(mock.call(["rebase", "--abort"]), git.call_args_list)
        self.assertIn("CONFLICT", errors.getvalue())
        self.assertEqual(doc.target.read_text(encoding="utf-8"), "local translation\n")
        self.assertEqual(self.git(self.root, "log", "-1", "--format=%s").stdout.strip(), "local translation")
        self.assertEqual(self.git(self.root, "status", "--porcelain").stdout, "")
        self.assertFalse((self.root / ".git/rebase-merge").exists())
        self.assertFalse((self.root / ".git/rebase-apply").exists())
        self.assertEqual((peer / doc.target_rel).read_text(encoding="utf-8"), "remote translation\n")

    def test_push_recovery_is_bounded_and_only_for_remote_advance(self) -> None:
        cases = (
            ("[rejected] (fetch first)", "", 3),
            ("[rejected] (non-fast-forward)", "", 3),
            ("[remote rejected] (protected branch hook declined)", "protected branch", 1),
            ("", "Permission denied", 1),
            ("", "Could not resolve host", 1),
        )
        for reason, stderr, expected_pushes in cases:
            with self.subTest(reason=reason, stderr=stderr):
                def git(args):
                    if args[0] == "push":
                        raise subprocess.CalledProcessError(
                            1, ["git", *args], output=f"!\trefs/heads/main:refs/heads/main\t{reason}\n", stderr=stderr,
                        )
                    return subprocess.CompletedProcess(args, 0, stdout="")

                with (
                    mock.patch.object(td, "run_git", side_effect=git) as run_git,
                    mock.patch.object(td, "has_staged_changes", return_value=True),
                    mock.patch.object(td, "log"),
                ):
                    with self.assertRaises(subprocess.CalledProcessError):
                        td.commit_translation([td.README_TARGET], "translation")
                commands = [call.args[0] for call in run_git.call_args_list]
                self.assertEqual(commands.count(["push", "--porcelain"]), expected_pushes)
                self.assertEqual(commands.count(["fetch"]), expected_pushes - 1)
                self.assertEqual(commands.count(["rebase", "@{upstream}"]), expected_pushes - 1)
                self.assertEqual(sum(command[0] == "commit" for command in commands), 1)

    def test_run_git_reports_stdout_and_stderr_without_wrapping_error(self) -> None:
        failure = subprocess.CalledProcessError(1, ["git", "push"], output="push rejected\n", stderr="reason\n")
        with (
            mock.patch.object(td.subprocess, "run", side_effect=failure),
            mock.patch.object(td.sys, "stderr", new_callable=io.StringIO) as errors,
        ):
            with self.assertRaises(subprocess.CalledProcessError) as caught:
                td.run_git(["push"])
        self.assertIs(caught.exception, failure)
        self.assertIn("push rejected", errors.getvalue())
        self.assertIn("reason", errors.getvalue())


if __name__ == "__main__":
    unittest.main()
