"""Deterministic release-boundary checks; no GitHub calls or publication."""

import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("release_plan", SCRIPTS / "release-plan.py")
release_plan = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release_plan)


class ReleasePlanTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        old = Path.cwd()
        os.chdir(self.directory.name)
        self.addCleanup(os.chdir, old)
        self.slot = patch.dict(os.environ, {"NEXT_VERSION_SLOT": "26.09"})
        self.slot.start()
        self.addCleanup(self.slot.stop)
        self.git("init", "--initial-branch=main")
        self.git("config", "user.name", "Release Test")
        self.git("config", "user.email", "test@example.invalid")
        Path("scripts").mkdir()
        shutil.copyfile(SCRIPTS / "next-version.sh", "scripts/next-version.sh")
        self.commit("cmd/bluefin-report/main.go", "package main\n")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")

    def git(self, *args):
        return subprocess.check_output(["git", *args], text=True, stderr=subprocess.DEVNULL).strip()

    def commit(self, path, text):
        target = Path(path)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text, encoding="utf-8")
        self.git("add", ".")
        self.git("commit", "-m", f"Update {path}")
        return self.git("rev-parse", "HEAD")

    def plan(self, event="schedule", ref="refs/heads/main", publish=False):
        return release_plan.plan(event, ref, self.git("rev-parse", "HEAD"), "main", publish)

    def test_first_release_uses_existing_calendar_script_without_mutating_tags(self):
        result = self.plan()
        self.assertEqual((result["run"], result["publish"], result["tag"]), ("true", "true", "26.09.1"))
        self.assertEqual(self.git("tag"), "")

    def test_legacy_v_tag_blocks_docs_and_screenshot_release_loop(self):
        self.git("tag", "v1.2.3")
        self.commit("README.md", "Documentation only\n")
        self.commit("data/screenshots/overview.png", "new image\n")
        self.commit("data/screenshots/provenance.json", "{}\n")
        result = self.plan()
        self.assertEqual(result["previous"], "v1.2.3")
        self.assertEqual((result["run"], result["publish"]), ("false", "false"))

    def test_runtime_and_release_inputs_are_relevant_but_images_are_not(self):
        for path in ("cmd/report/main.go", "internal/ui/ui.go", "data/icons/app.svg", "go.mod", "go.sum", "scripts/next-version.sh", ".goreleaser.yaml", ".github/workflows/release.yml"):
            with self.subTest(path=path):
                self.assertTrue(release_plan.relevant(path))
        for path in ("README.md", "CONTRIBUTING.md", "data/screenshots/review.png", "data/screenshots/provenance.json"):
            with self.subTest(path=path):
                self.assertFalse(release_plan.relevant(path))

    def test_calendar_sequence_and_prerelease_exclusion(self):
        self.git("tag", "26.09.2")
        self.git("tag", "26.09.99-alpha.1")
        self.commit("internal/new.go", "package internal\n")
        result = self.plan()
        self.assertEqual((result["previous"], result["tag"], result["publish"]), ("26.09.2", "26.09.3", "true"))

    def test_moving_runtime_source_out_of_release_paths_is_relevant(self):
        self.git("tag", "26.09.1")
        Path("docs").mkdir()
        self.git("mv", "cmd/bluefin-report/main.go", "docs/main.go")
        self.git("commit", "-m", "Move runtime source")
        self.assertEqual(self.plan()["publish"], "true")

    def test_feature_dry_run_builds_even_without_relevant_changes(self):
        self.git("tag", "26.09.1")
        self.commit("README.md", "feature docs\n")
        result = self.plan("workflow_dispatch", "refs/heads/feature")
        self.assertEqual((result["run"], result["publish"]), ("true", "false"))
        self.assertEqual(self.git("tag"), "26.09.1")

    def test_pr_and_merge_queue_previews_cannot_publish_even_when_requested(self):
        self.git("tag", "26.09.1")
        self.commit("README.md", "docs-only preview\n")
        for event, ref in (("pull_request", "refs/pull/17/merge"), ("merge_group", "refs/heads/gh-readonly-queue/main/pr-17")):
            with self.subTest(event=event):
                result = self.plan(event, ref, True)
                self.assertEqual((result["run"], result["publish"]), ("true", "false"))
        self.assertEqual(self.git("tag"), "26.09.1")

    def test_feature_publication_is_rejected(self):
        for event in ("workflow_dispatch", "schedule"):
            with self.subTest(event=event), self.assertRaises(ValueError):
                self.plan(event, "refs/heads/feature", True)

    def test_existing_numeric_tag_releases_without_allocating_new_tag(self):
        self.git("tag", "v1.0.0")
        self.commit("README.md", "manual release\n")
        self.git("tag", "26.09.7")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        result = self.plan("push", "refs/tags/26.09.7")
        self.assertEqual((result["tag"], result["previous"], result["publish"]), ("26.09.7", "v1.0.0", "true"))

    def test_untrusted_tag_commit_and_nonstable_tags_are_rejected(self):
        self.commit("internal/feature.go", "package internal\n")
        self.git("tag", "26.09.1")
        with self.assertRaises(subprocess.CalledProcessError):
            self.plan("push", "refs/tags/26.09.1")
        for tag in ("v26.09.1", "26.09.1-alpha", "26.09.1/evil"):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                self.plan("push", f"refs/tags/{tag}")

    def test_mismatched_event_sha_is_rejected(self):
        previous = self.git("rev-parse", "HEAD")
        self.commit("README.md", "another commit\n")
        with self.assertRaises(ValueError):
            release_plan.plan("workflow_dispatch", "refs/heads/main", previous, "main", False)


if __name__ == "__main__":
    unittest.main()
