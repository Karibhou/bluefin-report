#!/usr/bin/env python3
"""Propose released screenshots using a fixed bot branch and non-forced Git refs.

Run this local script from the trusted release checkout, never from the PR branch.
Only the four fixed data/screenshots paths can be written; README is not rewritten.
"""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
from urllib.parse import quote

BRANCH = "automation/readme-screenshots"
ASSETS = {
    "screenshot-overview.png": "data/screenshots/overview.png",
    "screenshot-metrics.png": "data/screenshots/metrics.png",
    "screenshot-review.png": "data/screenshots/review.png",
    "screenshot-provenance.json": "data/screenshots/provenance.json",
}


def api(path, method="GET", data=None, allow_missing=False):
    command = ["gh", "api", "--method", method, path]
    if data is not None:
        command += ["--input", "-"]
    result = subprocess.run(command, input=json.dumps(data) if data is not None else None, capture_output=True, text=True)
    if result.returncode:
        if allow_missing and "(HTTP 404)" in result.stderr:
            return None
        raise RuntimeError(f"GitHub API request failed: {method} {path}\n{result.stderr}")
    return json.loads(result.stdout) if result.stdout.strip() else None


def blob_sha(content):
    return hashlib.sha1(f"blob {len(content)}\0".encode() + content).hexdigest()


def load_assets(directory):
    assets = {}
    for source, target in ASSETS.items():
        path = directory / source
        if path.is_symlink() or not path.is_file():
            raise ValueError(f"missing regular screenshot asset: {path}")
        content = path.read_bytes()
        if source.endswith(".png") and not content.startswith(b"\x89PNG\r\n\x1a\n"):
            raise ValueError(f"not a PNG: {path}")
        if source.endswith(".json"):
            if not isinstance(json.loads(content), dict):
                raise ValueError("screenshot provenance must be a JSON object")
        assets[target] = content
    return assets


def tree_entries(repository, sha):
    commit = api(f"repos/{repository}/git/commits/{sha}")
    tree = api(f"repos/{repository}/git/trees/{commit['tree']['sha']}?recursive=1")
    if tree.get("truncated"):
        raise ValueError("repository tree is truncated; refusing an incomplete image comparison")
    return {entry["path"]: entry for entry in tree["tree"]}


def same_images(entries, assets):
    return all(entries.get(path, {}).get("sha") == blob_sha(content) for path, content in assets.items() if path.endswith(".png"))


def owned_pull(pull, repository, default, login, branch_sha):
    return (pull["user"]["login"] == login
            and pull["head"]["repo"] is not None
            and pull["head"]["repo"]["full_name"] == repository
            and pull["head"]["ref"] == BRANCH
            and pull["head"]["sha"] == branch_sha
            and pull["base"]["repo"]["full_name"] == repository
            and pull["base"]["ref"] == default)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--screenshots", required=True, type=Path)
    parser.add_argument("--release-tag", required=True)
    parser.add_argument("--source-sha", required=True)
    args = parser.parse_args()
    repository = os.environ.get("GH_REPO", "")
    slug = os.environ.get("APP_SLUG", "")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository) or not re.fullmatch(r"[A-Za-z0-9_-]+", slug):
        raise ValueError("GH_REPO and APP_SLUG must identify the repository and installed GitHub App")
    if not os.environ.get("GH_TOKEN"):
        raise ValueError("GH_TOKEN must be an App installation token with Contents and Pull requests write permissions")
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", args.release_tag) or not re.fullmatch(r"[0-9a-f]{40}", args.source_sha):
        raise ValueError("expected a stable numeric release tag and immutable commit SHA")
    assets = load_assets(args.screenshots)
    repo = api(f"repos/{repository}")
    default = repo["default_branch"]
    default_ref = api(f"repos/{repository}/git/ref/heads/{quote(default, safe='')}")
    default_sha = default_ref["object"]["sha"]
    # Ref data is fetched from GitHub, not inferred from branch names or workflow outputs.
    tag_ref = api(f"repos/{repository}/git/ref/tags/{args.release_tag}")
    tag_object = tag_ref["object"]
    while tag_object["type"] == "tag":
        tag_object = api(f"repos/{repository}/git/tags/{tag_object['sha']}")["object"]
    if tag_object["type"] != "commit" or tag_object["sha"] != args.source_sha:
        raise ValueError("release tag no longer matches the trusted release source")
    release = api(f"repos/{repository}/releases/tags/{args.release_tag}")
    if release["draft"] or release["prerelease"]:
        raise ValueError("README images can only be refreshed from a published stable release")
    default_entries = tree_entries(repository, default_sha)
    login = f"{slug}[bot]"
    owner = repository.split("/")[0]
    pulls = api(f"repos/{repository}/pulls?state=open&head={quote(owner + ':' + BRANCH, safe='')}&per_page=100")
    if len(pulls) > 1:
        raise ValueError("multiple open screenshot PRs found; refusing an ambiguous update")
    pull = pulls[0] if pulls else None
    branch_ref = api(f"repos/{repository}/git/ref/heads/{quote(BRANCH, safe='')}", allow_missing=True)
    branch_sha = branch_ref["object"]["sha"] if branch_ref else None
    if pull and not owned_pull(pull, repository, default, login, branch_sha):
        raise ValueError("existing screenshot PR does not match the configured App, repository, base and branch head")
    default_matches = same_images(default_entries, assets)
    if default_matches and not pull:
        print("Released images already match the default branch; no branch or PR changes.")
        return
    parents = [default_sha]
    if branch_ref:
        comparison = api(f"repos/{repository}/compare/{default_sha}...{branch_sha}")
        if comparison["status"] in {"ahead", "diverged"}:
            head = api(f"repos/{repository}/commits/{branch_sha}")
            if not head.get("author") or head["author"]["login"] != login:
                raise ValueError("existing automation branch head is not owned by the configured App")
            if not pull:
                closed = api(f"repos/{repository}/pulls?state=closed&head={quote(owner + ':' + BRANCH, safe='')}&per_page=100")
                matches = [item for item in closed if owned_pull(item, repository, default, login, branch_sha)]
                if len(closed) >= 100 or len(matches) != 1:
                    raise ValueError("existing automation branch has no unambiguous owned PR at its exact head")
            if comparison["total_commits"] > 250 or len(comparison.get("files", [])) >= 300:
                raise ValueError("automation branch diff exceeds safe comparison limits")
            if any(file["filename"] not in ASSETS.values() or file.get("previous_filename", file["filename"]) not in ASSETS.values() for file in comparison.get("files", [])):
                raise ValueError("automation branch contains changes outside the four screenshot assets")
            # Keep the retained branch in history while taking all non-asset content
            # from the current default tree, including after squash/rebase merges.
            parents = [branch_sha, default_sha]
        elif comparison["status"] not in {"behind", "identical"}:
            raise ValueError("cannot safely fast-forward the existing automation branch")
    if default_matches:
        api(f"repos/{repository}/pulls/{pull['number']}", "PATCH", {"state": "closed"})
        print("Released images already match the default branch; closed the obsolete bot PR.")
        return
    if pull and comparison["status"] in {"ahead", "diverged"} and same_images(tree_entries(repository, branch_sha), assets):
        print(f"Released images already match the open bot PR: {pull['html_url']}")
        return
    entries = default_entries
    # Never overwrite symlinks, submodules or directories with screenshot assets.
    for path in assets:
        if path in entries and (entries[path]["type"] != "blob" or entries[path]["mode"] != "100644"):
            raise ValueError(f"unsafe tracked screenshot asset: {path}")
    bot = api(f"users/{quote(login, safe='')}")
    email = f"{bot['id']}+{login}@users.noreply.github.com"
    changed = [path for path, content in assets.items() if entries.get(path, {}).get("sha") != blob_sha(content)]
    if changed:
        tree = []
        for path in changed:
            blob = api(f"repos/{repository}/git/blobs", "POST", {"content": base64.b64encode(assets[path]).decode(), "encoding": "base64"})
            tree.append({"path": path, "mode": "100644", "type": "blob", "sha": blob["sha"]})
        parent_commit = api(f"repos/{repository}/git/commits/{default_sha}")
        new_tree = api(f"repos/{repository}/git/trees", "POST", {"base_tree": parent_commit["tree"]["sha"], "tree": tree})
        commit = api(f"repos/{repository}/git/commits", "POST", {
            "message": f"docs: refresh screenshots for {args.release_tag}",
            "tree": new_tree["sha"], "parents": parents,
            "author": {"name": login, "email": email},
            "committer": {"name": login, "email": email},
        })
        if branch_ref:
            # Never force: a concurrent or human branch update fails rather than being lost.
            api(f"repos/{repository}/git/refs/heads/{quote(BRANCH, safe='')}", "PATCH", {"sha": commit["sha"], "force": False})
        else:
            api(f"repos/{repository}/git/refs", "POST", {"ref": f"refs/heads/{BRANCH}", "sha": commit["sha"]})
    body = (f"Refresh the README's tracked images from release [{args.release_tag}](https://github.com/{repository}/releases/tag/{args.release_tag}).\n\n"
            f"Captured live from immutable source `{args.source_sha}` after the release quality gates. "
            "Only `data/screenshots/` images and their provenance are changed; README text is untouched. "
            "These paths are excluded from release change detection, so merging this PR cannot create a release loop.")
    for label, filename in (("Overview", "screenshot-overview.png"),
                            ("Metrics", "screenshot-metrics.png"),
                            ("Review", "screenshot-review.png")):
        body += f"\n\n![{label}](https://github.com/{repository}/releases/download/{args.release_tag}/{filename})"
    data = {"title": f"docs: refresh screenshots for {args.release_tag}", "body": body}
    if pull:
        result = api(f"repos/{repository}/pulls/{pull['number']}", "PATCH", data)
    else:
        result = api(f"repos/{repository}/pulls", "POST", {**data, "head": BRANCH, "base": default})
    print(result["html_url"])


if __name__ == "__main__":
    main()
