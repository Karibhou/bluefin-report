"""Exercise PR write boundaries with a deterministic GitHub API double."""

import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("update_images", Path(__file__).with_name("update-readme-screenshots.py"))
updater = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(updater)


class ScreenshotUpdateTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.directory = Path(directory.name)
        for name in updater.ASSETS:
            content = b"\x89PNG\r\n\x1a\nimage" if name.endswith(".png") else b'{"source":"release"}'
            (self.directory / name).write_bytes(content)
        self.source = "a" * 40
        self.default = "b" * 40
        self.branch = "c" * 40
        self.calls = []
        self.existing = False
        self.matching_default = False
        self.matching_branch = False
        self.unrelated = False
        self.draft = False
        self.tag_sha = self.source
        self.pull = {
            "number": 17, "user": {"login": "mergeraptor[bot]"},
            "head": {"repo": {"full_name": "owner/report"}},
            "base": {"ref": "main"}, "html_url": "https://example.invalid/pr/17",
        }
        environment = patch.dict(os.environ, {"GH_REPO": "owner/report", "APP_SLUG": "mergeraptor", "GH_TOKEN": "test-only-token"})
        environment.start()
        self.addCleanup(environment.stop)
        argv = patch("sys.argv", ["update-readme-screenshots.py", "--screenshots", str(self.directory), "--release-tag", "26.09.1", "--source-sha", self.source])
        argv.start()
        self.addCleanup(argv.stop)
        mocked = patch.object(updater, "api", self.api)
        mocked.start()
        self.addCleanup(mocked.stop)

    def api(self, path, method="GET", data=None, allow_missing=False):
        self.calls.append((path, method, data))
        root = "repos/owner/report"
        if method == "GET":
            if path == root:
                return {"default_branch": "main"}
            if path == f"{root}/git/ref/heads/main":
                return {"object": {"sha": self.default}}
            if path == f"{root}/git/ref/tags/26.09.1":
                return {"object": {"type": "commit", "sha": self.tag_sha}}
            if path == f"{root}/releases/tags/26.09.1":
                return {"draft": self.draft, "prerelease": False}
            if path.startswith(f"{root}/git/commits/"):
                return {"tree": {"sha": path.rsplit("/", 1)[1] + "-tree"}}
            if path.startswith(f"{root}/git/trees/"):
                matching = self.matching_default if self.default in path else self.matching_branch
                entries = []
                if matching:
                    for name, target in updater.ASSETS.items():
                        sha = updater.blob_sha((self.directory / name).read_bytes()) if name.endswith(".png") else "old-provenance"
                        entries.append({"path": target, "sha": sha, "mode": "100644", "type": "blob"})
                return {"tree": entries, "truncated": False}
            if path == "users/mergeraptor%5Bbot%5D":
                return {"id": 123}
            if path.startswith(f"{root}/pulls?"):
                return [self.pull] if self.existing else []
            if path == f"{root}/git/ref/heads/automation%2Freadme-screenshots":
                return {"object": {"sha": self.branch}} if self.existing else None
            if path == f"{root}/compare/{self.default}...{self.branch}":
                return {"status": "ahead", "total_commits": 1, "files": [{"filename": "README.md" if self.unrelated else "data/screenshots/overview.png"}]}
            if path == f"{root}/commits/{self.branch}":
                return {"author": {"login": "mergeraptor[bot]"}}
        elif method == "POST" and path == f"{root}/git/blobs":
            return {"sha": "blob-sha"}
        elif method == "POST" and path == f"{root}/git/trees":
            return {"sha": "new-tree"}
        elif method == "POST" and path == f"{root}/git/commits":
            return {"sha": "new-commit"}
        elif method in {"POST", "PATCH"} and "/git/refs" in path:
            return {}
        elif method in {"POST", "PATCH"} and "/pulls" in path:
            return {"html_url": "https://example.invalid/pr/17"}
        raise AssertionError(f"unexpected API request: {method} {path} {json.dumps(data)}")

    def writes(self):
        return [(path, method, data) for path, method, data in self.calls if method != "GET"]

    def test_new_pr_writes_only_fixed_assets_and_never_default_branch(self):
        updater.main()
        trees = [data for path, method, data in self.writes() if path.endswith("/git/trees")]
        self.assertEqual({entry["path"] for entry in trees[0]["tree"]}, {
            "data/screenshots/overview.png",
            "data/screenshots/metrics.png",
            "data/screenshots/review.png",
            "data/screenshots/provenance.json",
        })
        refs = [data for path, method, data in self.writes() if "/git/refs" in path]
        self.assertEqual(refs, [{"ref": "refs/heads/automation/readme-screenshots", "sha": "new-commit"}])
        self.assertEqual([method for path, method, data in self.writes() if path.endswith("/pulls")], ["POST"])

    def test_existing_owned_pr_uses_nonforced_update_and_reuses_pr(self):
        self.existing = True
        updater.main()
        refs = [(path, data) for path, method, data in self.writes() if "/git/refs" in path]
        self.assertEqual(refs, [("repos/owner/report/git/refs/heads/automation%2Freadme-screenshots", {"sha": "new-commit", "force": False})])
        self.assertTrue(any(path.endswith("/pulls/17") and method == "PATCH" for path, method, data in self.writes()))
        self.assertFalse(any(path.endswith("/pulls") for path, method, data in self.writes()))

    def test_identical_default_images_are_noop_even_if_provenance_differs(self):
        self.matching_default = True
        updater.main()
        self.assertEqual(self.writes(), [])

    def test_identical_open_pr_images_are_noop(self):
        self.existing = True
        self.matching_branch = True
        updater.main()
        self.assertEqual(self.writes(), [])

    def test_arbitrary_branch_content_is_rejected_before_any_write(self):
        self.existing = True
        self.unrelated = True
        with self.assertRaises(ValueError):
            updater.main()
        self.assertEqual(self.writes(), [])

    def test_human_owned_pr_is_rejected(self):
        self.existing = True
        self.pull["user"]["login"] = "human"
        with self.assertRaises(ValueError):
            updater.main()
        self.assertEqual(self.writes(), [])

    def test_draft_release_or_moved_tag_cannot_update_images(self):
        self.draft = True
        with self.assertRaises(ValueError):
            updater.main()
        self.assertEqual(self.writes(), [])
        self.draft = False
        self.tag_sha = "d" * 40
        with self.assertRaises(ValueError):
            updater.main()
        self.assertEqual(self.writes(), [])


if __name__ == "__main__":
    unittest.main()
