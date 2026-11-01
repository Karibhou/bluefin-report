"""Exercise screenshot PR lifecycles with temporary Git and a recording GitHub API."""

import base64
import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, unquote, urlsplit

SPEC = importlib.util.spec_from_file_location("update_images", Path(__file__).with_name("update-readme-screenshots.py"))
updater = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(updater)

# Deliberately independent of the implementation's allowlist.
ASSETS = {
    "screenshot-overview.png": "data/screenshots/overview.png",
    "screenshot-metrics.png": "data/screenshots/metrics.png",
    "screenshot-review.png": "data/screenshots/review.png",
    "screenshot-provenance.json": "data/screenshots/provenance.json",
}
BOT_BRANCH = "automation/readme-screenshots"
BOT_LOGIN = "mergeraptor[bot]"
ROOT = "repos/owner/report"


class ScreenshotUpdateTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name) / "assets"
        self.directory.mkdir()
        self.repository = Path(temporary.name) / "repo"
        self.repository.mkdir()
        self.git("init", "--initial-branch=main")
        self.git("config", "user.name", BOT_LOGIN)
        self.git("config", "user.email", "123+mergeraptor[bot]@users.noreply.github.com")
        self.initial_assets = self.asset_contents(b"initial")
        initial_tree = self.make_tree(None, {**self.initial_assets, "README.md": b"original README\n"})
        self.source = self.commit(initial_tree, [])
        self.git("update-ref", "refs/heads/main", self.source)
        self.set_release(b"release-one")
        self.calls = []
        self.pulls = []
        self.draft = False
        self.tag_sha = self.source
        environment = patch.dict(os.environ, {"GH_REPO": "owner/report", "APP_SLUG": "mergeraptor", "GH_TOKEN": "test-only-token"})
        environment.start()
        self.addCleanup(environment.stop)
        argv = patch("sys.argv", ["update-readme-screenshots.py", "--screenshots", str(self.directory), "--release-tag", "26.09.1", "--source-sha", self.source])
        argv.start()
        self.addCleanup(argv.stop)
        mocked = patch.object(updater, "api", self.api)
        mocked.start()
        self.addCleanup(mocked.stop)

    def git(self, *arguments, content=None, environment=None):
        result = subprocess.run(["git", "-C", str(self.repository), *arguments], input=content,
                                capture_output=True, check=True, env=environment)
        return result.stdout.decode().strip()

    def asset_contents(self, suffix):
        return {target: (b"\x89PNG\r\n\x1a\n" + suffix if name.endswith(".png")
                         else json.dumps({"capture": suffix.decode()}).encode())
                for name, target in ASSETS.items()}

    def set_release(self, suffix):
        contents = self.asset_contents(suffix)
        for name, target in ASSETS.items():
            (self.directory / name).write_bytes(contents[target])

    def make_tree(self, base, changes):
        entries = [{"path": path, "mode": "100644", "type": "blob",
                    "sha": self.git("hash-object", "-w", "--stdin", content=content)}
                   for path, content in changes.items()]
        return self.write_tree(base, entries)

    def write_tree(self, base, entries):
        with tempfile.TemporaryDirectory() as temporary:
            environment = {**os.environ, "GIT_INDEX_FILE": str(Path(temporary) / "index")}
            self.git("read-tree", base or "--empty", environment=environment)
            for entry in entries:
                self.git("update-index", "--add", "--cacheinfo", f"{entry['mode']},{entry['sha']},{entry['path']}", environment=environment)
            return self.git("write-tree", environment=environment)

    def commit(self, tree, parents, human=False):
        environment = None
        if human:
            environment = {**os.environ, "GIT_AUTHOR_NAME": "Human", "GIT_AUTHOR_EMAIL": "human@example.invalid"}
        arguments = ["commit-tree", tree]
        for parent in parents:
            arguments += ["-p", parent]
        return self.git(*arguments, content=b"screenshot lifecycle fixture\n", environment=environment)

    def head(self, branch="main"):
        return self.git("rev-parse", f"refs/heads/{branch}")

    def advance(self, branch, changes, human=False):
        parent = self.head(branch)
        commit = self.commit(self.make_tree(parent, changes), [parent], human=human)
        self.git("update-ref", f"refs/heads/{branch}", commit, parent)
        for pull in self.pulls:
            if pull["state"] == "open" and pull["head"]["ref"] == branch:
                pull["head"]["sha"] = commit
        return commit

    def close_pull(self, merged=False):
        self.pulls[-1]["state"] = "closed"
        self.pulls[-1]["merged"] = merged

    def api(self, path, method="GET", data=None, allow_missing=False):
        self.calls.append((path, method, copy.deepcopy(data)))
        parsed = urlsplit(path)
        endpoint = unquote(parsed.path)
        if method == "GET":
            if endpoint == ROOT:
                return {"default_branch": "main"}
            if endpoint.startswith(f"{ROOT}/git/ref/heads/"):
                branch = endpoint.split("/git/ref/heads/", 1)[1]
                result = subprocess.run(["git", "-C", str(self.repository), "rev-parse", "--verify", f"refs/heads/{branch}"], capture_output=True, text=True)
                if result.returncode:
                    if allow_missing:
                        return None
                    raise AssertionError(f"missing branch: {branch}")
                return {"object": {"sha": result.stdout.strip()}}
            if endpoint == f"{ROOT}/git/ref/tags/26.09.1":
                return {"object": {"type": "commit", "sha": self.tag_sha}}
            if endpoint == f"{ROOT}/releases/tags/26.09.1":
                return {"draft": self.draft, "prerelease": False}
            if endpoint.startswith(f"{ROOT}/git/commits/"):
                sha = endpoint.rsplit("/", 1)[1]
                return {"tree": {"sha": self.git("rev-parse", f"{sha}^{{tree}}")}}
            if endpoint.startswith(f"{ROOT}/git/trees/"):
                sha = endpoint.rsplit("/", 1)[1]
                entries = []
                for line in self.git("ls-tree", "-r", "-t", sha).splitlines():
                    metadata, name = line.split("\t", 1)
                    mode, kind, object_sha = metadata.split()
                    entries.append({"path": name, "mode": mode, "type": kind, "sha": object_sha})
                return {"tree": entries, "truncated": False}
            if endpoint == "users/mergeraptor[bot]":
                return {"id": 123}
            if endpoint == f"{ROOT}/pulls":
                query = parse_qs(parsed.query)
                self.assertEqual(query["head"], [f"owner:{BOT_BRANCH}"])
                return copy.deepcopy([pull for pull in self.pulls if pull["state"] == query["state"][0]])
            if endpoint.startswith(f"{ROOT}/compare/"):
                base, head = endpoint.split("/compare/", 1)[1].split("...")
                common = self.git("merge-base", base, head)
                status = "identical" if base == head else "ahead" if common == base else "behind" if common == head else "diverged"
                files = []
                for line in self.git("diff", "--name-status", "-M", common, head).splitlines():
                    change, *names = line.split("\t")
                    item = {"filename": names[-1]}
                    if change.startswith("R"):
                        item["previous_filename"] = names[0]
                    files.append(item)
                return {"status": status, "total_commits": int(self.git("rev-list", "--count", f"{base}..{head}")), "files": files}
            if endpoint.startswith(f"{ROOT}/commits/"):
                email = self.git("show", "-s", "--format=%ae", endpoint.rsplit("/", 1)[1])
                return {"author": {"login": BOT_LOGIN if email == "123+mergeraptor[bot]@users.noreply.github.com" else "human"}}
        elif method == "POST" and endpoint == f"{ROOT}/git/blobs":
            return {"sha": self.git("hash-object", "-w", "--stdin", content=base64.b64decode(data["content"]))}
        elif method == "POST" and endpoint == f"{ROOT}/git/trees":
            return {"sha": self.write_tree(data["base_tree"], data["tree"])}
        elif method == "POST" and endpoint == f"{ROOT}/git/commits":
            return {"sha": self.commit(data["tree"], data["parents"])}
        elif method == "POST" and endpoint == f"{ROOT}/git/refs":
            self.assertEqual(data["ref"], f"refs/heads/{BOT_BRANCH}")
            self.git("update-ref", data["ref"], data["sha"], "0" * 40)
            return {}
        elif method == "PATCH" and endpoint.startswith(f"{ROOT}/git/refs/heads/"):
            self.assertEqual(endpoint, f"{ROOT}/git/refs/heads/{BOT_BRANCH}")
            self.assertIs(data["force"], False)
            previous = self.head(BOT_BRANCH)
            self.git("merge-base", "--is-ancestor", previous, data["sha"])
            self.git("update-ref", f"refs/heads/{BOT_BRANCH}", data["sha"], previous)
            for pull in self.pulls:
                if pull["state"] == "open":
                    pull["head"]["sha"] = data["sha"]
            return {}
        elif method == "POST" and endpoint == f"{ROOT}/pulls":
            self.assertEqual(data["head"], BOT_BRANCH)
            self.assertEqual(data["base"], "main")
            pull = {"number": len(self.pulls) + 1, "state": "open", "merged": False,
                    "user": {"login": BOT_LOGIN},
                    "head": {"repo": {"full_name": "owner/report"}, "ref": BOT_BRANCH, "sha": self.head(BOT_BRANCH)},
                    "base": {"repo": {"full_name": "owner/report"}, "ref": "main"},
                    "html_url": f"https://example.invalid/pr/{len(self.pulls) + 1}",
                    "title": data["title"], "body": data["body"]}
            self.pulls.append(pull)
            return copy.deepcopy(pull)
        elif method == "PATCH" and endpoint.startswith(f"{ROOT}/pulls/"):
            pull = next(pull for pull in self.pulls if pull["number"] == int(endpoint.rsplit("/", 1)[1]))
            pull.update(data)
            return copy.deepcopy(pull)
        raise AssertionError(f"unexpected API request: {method} {path} {json.dumps(data)}")

    def writes(self):
        return [(path, method, data) for path, method, data in self.calls if method != "GET"]

    def assert_asset_update(self, default, old_branch=None):
        branch = self.head(BOT_BRANCH)
        self.assertEqual(self.head(), default)
        self.assertEqual(set(self.git("diff", "--name-only", default, branch).splitlines()), set(ASSETS.values()))
        parents = self.git("show", "-s", "--format=%P", branch).split()
        self.assertEqual(parents, [old_branch, default] if old_branch else [default])
        for name, target in ASSETS.items():
            self.assertEqual(self.git("rev-parse", f"{branch}:{target}"), self.git("hash-object", "--stdin", content=(self.directory / name).read_bytes()))
        self.assertEqual(self.git("show", f"{branch}:README.md"), self.git("show", f"{default}:README.md"))
        for path, method, data in self.writes():
            if path.endswith("/git/trees"):
                self.assertLessEqual({entry["path"] for entry in data["tree"]}, set(ASSETS.values()))
            if "/git/refs/heads/" in path:
                self.assertEqual(data["force"], False)

    def test_new_pr_writes_only_fixed_assets_and_never_default_branch(self):
        updater.main()
        self.assert_asset_update(self.source)
        self.assertEqual(len(self.pulls), 1)

    def test_existing_owned_pr_uses_nonforced_update_and_reuses_pr(self):
        updater.main()
        old_branch = self.head(BOT_BRANCH)
        default = self.advance("main", {"README.md": b"new default documentation\n"})
        self.set_release(b"release-two")
        self.calls.clear()
        updater.main()
        self.assert_asset_update(default, old_branch)
        self.assertEqual(len(self.pulls), 1)
        self.assertTrue(any(path.endswith("/pulls/1") and method == "PATCH" for path, method, data in self.writes()))

    def test_retained_squash_merged_branch_refreshes_from_current_default(self):
        updater.main()
        old_branch = self.head(BOT_BRANCH)
        # A squash/rebase merge copies the images without making the bot head an ancestor.
        merged = self.commit(self.git("rev-parse", f"{old_branch}^{{tree}}"), [self.head()], human=True)
        self.git("update-ref", "refs/heads/main", merged)
        self.close_pull(merged=True)
        default = self.advance("main", {"README.md": b"documentation after squash merge\n"})
        self.set_release(b"release-two")
        self.calls.clear()
        updater.main()
        self.assert_asset_update(default, old_branch)
        self.assertEqual([pull["state"] for pull in self.pulls], ["closed", "open"])

    def test_matching_images_on_branch_behind_default_still_refresh_default_tree(self):
        updater.main()
        self.git("update-ref", "refs/heads/main", self.head(BOT_BRANCH))
        default = self.advance("main", self.asset_contents(b"different-default"))
        self.calls.clear()
        updater.main()
        self.assert_asset_update(default)
        self.assertEqual(len(self.pulls), 1)

    def make_stale_pull(self):
        updater.main()
        old_branch = self.head(BOT_BRANCH)
        self.set_release(b"initial")
        self.calls.clear()
        return old_branch

    def test_latest_default_images_close_obsolete_owned_open_pr(self):
        old_branch = self.make_stale_pull()
        updater.main()
        self.assertEqual(self.head(), self.source)
        self.assertEqual(self.head(BOT_BRANCH), old_branch)
        self.assertEqual(self.pulls[0]["state"], "closed")
        self.assertEqual(self.writes(), [(f"{ROOT}/pulls/1", "PATCH", {"state": "closed"})])

    def test_future_changed_release_refreshes_after_obsolete_pr_closed(self):
        old_branch = self.make_stale_pull()
        updater.main()
        self.set_release(b"release-three")
        self.calls.clear()
        updater.main()
        self.assert_asset_update(self.source, old_branch)
        self.assertEqual([pull["state"] for pull in self.pulls], ["closed", "open"])

    def test_identical_default_images_are_noop_even_if_provenance_differs(self):
        self.set_release(b"initial")
        (self.directory / "screenshot-provenance.json").write_text('{"different":"provenance"}')
        updater.main()
        self.assertEqual(self.writes(), [])

    def test_identical_open_pr_images_are_noop_even_if_provenance_differs(self):
        updater.main()
        (self.directory / "screenshot-provenance.json").write_text('{"different":"provenance"}')
        self.calls.clear()
        updater.main()
        self.assertEqual(self.writes(), [])

    def test_arbitrary_branch_content_is_rejected_before_any_write(self):
        updater.main()
        self.advance(BOT_BRANCH, {"README.md": b"unrelated branch content\n"})
        self.set_release(b"release-two")
        self.calls.clear()
        with self.assertRaises(ValueError):
            updater.main()
        self.assertEqual(self.writes(), [])

    def test_human_owned_pr_is_rejected_even_when_default_images_match(self):
        self.make_stale_pull()
        self.pulls[0]["user"]["login"] = "human"
        with self.assertRaises(ValueError):
            updater.main()
        self.assertEqual(self.writes(), [])

    def test_human_branch_head_is_rejected(self):
        updater.main()
        self.advance(BOT_BRANCH, {"data/screenshots/overview.png": b"human work"}, human=True)
        self.set_release(b"release-two")
        self.calls.clear()
        with self.assertRaises(ValueError):
            updater.main()
        self.assertEqual(self.writes(), [])

    def test_closed_pr_requires_exact_bot_head_repository_and_base(self):
        updater.main()
        self.close_pull(merged=True)
        self.set_release(b"release-two")
        original = copy.deepcopy(self.pulls[0])
        for keys, value in ((["head", "sha"], self.source),
                            (["head", "repo", "full_name"], "other/report"),
                            (["head", "ref"], "other-branch"),
                            (["base", "repo", "full_name"], "other/report"),
                            (["base", "ref"], "other-base"),
                            (["user", "login"], "human")):
            with self.subTest(field=keys):
                self.pulls[0] = copy.deepcopy(original)
                target = self.pulls[0]
                for key in keys[:-1]:
                    target = target[key]
                target[keys[-1]] = value
                self.calls.clear()
                with self.assertRaises(ValueError):
                    updater.main()
                self.assertEqual(self.writes(), [])

    def test_ambiguous_closed_prs_are_rejected(self):
        updater.main()
        self.close_pull()
        self.pulls.append(copy.deepcopy(self.pulls[0]))
        self.set_release(b"release-two")
        self.calls.clear()
        with self.assertRaises(ValueError):
            updater.main()
        self.assertEqual(self.writes(), [])

    def test_closed_pr_does_not_authorize_outside_asset_changes(self):
        updater.main()
        self.advance(BOT_BRANCH, {"README.md": b"unexpected documentation\n"})
        self.close_pull(merged=True)
        self.set_release(b"release-two")
        self.calls.clear()
        with self.assertRaises(ValueError):
            updater.main()
        self.assertEqual(self.writes(), [])

    def test_stale_pr_head_must_match_branch_before_closure(self):
        self.make_stale_pull()
        self.pulls[0]["head"]["sha"] = self.source
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
