#!/usr/bin/env python3
"""Capture the real GTK app using AT-SPI and repository-only telemetry fixtures.

Run under xvfb-run and dbus-run-session. Never publishes reports. Optional
SCREENSHOT_LIBRARY_PATH selects a non-system puregotk runtime for local captures;
it is applied only to the application, never the D-Bus/accessibility daemons.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

import pyatspi
from gi.repository import GLib

ROOT = Path(__file__).resolve().parents[1]
IMAGES = ("screenshot-overview.png", "screenshot-metrics.png", "screenshot-review.png")


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def walk(node):
    yield node
    for child in node:
        if child is not None:
            yield from walk(child)


def wait_for(check, description, process, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        context = GLib.MainContext.default()
        while context.pending():
            context.iteration(False)
        if process.poll() is not None:
            raise RuntimeError(f"application exited with {process.returncode}: {description}")
        result = check()
        if result is not None and result is not False:
            return result
        time.sleep(0.1)
    raise RuntimeError(f"timed out waiting for {description}")


def named(root, name, role=None):
    for node in walk(root):
        if node.name == name and (role is None or node.getRole() == role):
            return node
    return None


def focus(node):
    # Use real keyboard traversal. GTK's AT-SPI component/action methods are
    # unavailable for some widgets; the focused state remains observable.
    context = GLib.MainContext.default()
    window = node
    while window.getRole() != pyatspi.ROLE_FRAME:
        window = window.parent
    for _ in range(80):
        while context.pending():
            context.iteration(False)
        if any(child.getState().contains(pyatspi.STATE_FOCUSED) for child in walk(node)):
            return
        current = next((child for child in walk(window)
                        if child.getState().contains(pyatspi.STATE_FOCUSED)), None)
        key = "ctrl+Tab" if current is not None and current.getState().contains(pyatspi.STATE_MULTI_LINE) else "Tab"
        subprocess.run(["xdotool", "key", key], check=True)
        time.sleep(0.05)
    raise RuntimeError(f"could not reach {node.name} using the keyboard")


def activate(node):
    focus(node)
    subprocess.run(["xdotool", "key", "Return"], check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=ROOT / "build/bluefin-report")
    parser.add_argument("--output", type=Path, default=ROOT / "build/screenshots")
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    collector = ROOT / "scripts/screenshot-collector.py"

    with tempfile.TemporaryDirectory(prefix="bluefin-screenshots-") as temporary:
        home = Path(temporary)
        environment = os.environ.copy()
        environment.update(
            HOME=str(home),
            XDG_CONFIG_HOME=str(home / "config"),
            XDG_STATE_HOME=str(home / "state"),
            XDG_DATA_HOME=str(home / "data"),
            XDG_CACHE_HOME=str(home / "cache"),
            GTK_A11Y="atspi",
            GDK_BACKEND="x11",
            GSK_RENDERER="cairo",
            LANG="C.UTF-8",
            LC_ALL="C.UTF-8",
            TZ="UTC",
            BLUEFIN_REPORT_SYSTEMD_REPORT=str(collector),
        )
        if library_path := os.environ.get("SCREENSHOT_LIBRARY_PATH"):
            environment["PUREGOTK_LIB_FOLDER"] = library_path
            environment["LD_LIBRARY_PATH"] = library_path
        log_path = home / "application.log"
        # GTK_THEME overrides libadwaita's stylesheet rather than selecting
        # the app's normal light appearance.
        environment.pop("GTK_THEME", None)
        with log_path.open("w+") as log:
            process = subprocess.Popen([str(binary), "--dry-run"], env=environment, stdout=log, stderr=log)
            try:
                desktop = pyatspi.Registry.getDesktop(0)
                app = wait_for(
                    lambda: next((node for node in desktop if node.get_process_id() == process.pid), None),
                    "application accessibility registration", process,
                )
                frame = wait_for(lambda: next((node for node in app if node.getRole() == pyatspi.ROLE_FRAME), None),
                                 "application window", process)
                wait_for(lambda: named(frame, "dakota-gaming"), "fixture collection", process)
                windows = subprocess.check_output(
                    ["xdotool", "search", "--onlyvisible", "--pid", str(process.pid)], text=True,
                ).splitlines()
                if len(windows) != 1:
                    raise RuntimeError(f"expected one application window, found {windows}")
                window = windows[0]
                subprocess.run(["xdotool", "windowsize", window, "900", "980"], check=True)
                subprocess.run(["xdotool", "windowfocus", "--sync", window], check=True)

                def capture(filename):
                    # Let GTK finish layout/navigation animation after semantic state assertions.
                    time.sleep(0.5)
                    path = output / filename
                    subprocess.run(["import", "-window", window, "-strip", str(path)], check=True)
                    dimensions = subprocess.check_output(["identify", "-format", "%wx%h", str(path)], text=True)
                    if dimensions != "900x980":
                        raise RuntimeError(f"wrong capture dimensions for {filename}: {dimensions}")
                    print(f"Captured {filename}: {dimensions}", flush=True)

                capture(IMAGES[0])
                activate(named(frame, "Browse all metrics", pyatspi.ROLE_LIST_ITEM))
                wait_for(lambda: named(frame, "Metrics"), "Metrics navigation", process)
                wait_for(lambda: named(frame, "io.systemd.Basic.Architecture"), "populated metrics", process)
                capture(IMAGES[1])

                subprocess.run(["xdotool", "key", "alt+Left"], check=True)
                wait_for(lambda: named(frame, "Report a Problem", pyatspi.ROLE_PUSH_BUTTON), "Overview navigation", process)
                activate(named(frame, "Report a Problem", pyatspi.ROLE_PUSH_BUTTON))
                title = wait_for(
                    lambda: next((node for node in walk(frame) if node.name == "Title" and
                                  node.getRole() in (pyatspi.ROLE_ENTRY, pyatspi.ROLE_TEXT)), None),
                    "report title entry", process,
                )
                focus(title)
                subprocess.run(["xdotool", "key", "ctrl+a"], check=True)
                subprocess.run(["xdotool", "type", "--clearmodifiers",
                                "Example: network connection stopped working"], check=True)
                review = wait_for(lambda: named(frame, "Review", pyatspi.ROLE_PUSH_BUTTON), "Review control", process)
                wait_for(lambda: review.getState().contains(pyatspi.STATE_SENSITIVE), "enabled Review control", process)
                activate(review)
                wait_for(lambda: named(frame, "Consent"), "review and consent page", process)
                capture(IMAGES[2])
            except Exception:
                log.flush()
                print(log_path.read_text(), flush=True)
                raise
            finally:
                if process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()

    fixtures = ROOT / "internal/snapshot/testdata"
    provenance = {
        "source_commit": os.environ.get("GITHUB_SHA") or subprocess.check_output(
            ["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
        "binary_sha256": digest(binary),
        "collector_sha256": digest(collector),
        "telemetry": "Repository fixtures only; not host data. Application ran with --dry-run.",
        "viewport": {"width": 900, "height": 980},
        "fixtures": {path.name: digest(path) for path in sorted(fixtures.iterdir()) if path.is_file()},
        "screenshots": {name: digest(output / name) for name in IMAGES},
    }
    (output / "screenshot-provenance.json").write_text(json.dumps(provenance, indent=2) + "\n")


if __name__ == "__main__":
    main()
