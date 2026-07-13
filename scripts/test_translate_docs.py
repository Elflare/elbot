import argparse
import json
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


if __name__ == "__main__":
    unittest.main()
