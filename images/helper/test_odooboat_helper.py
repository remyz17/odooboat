"""Helper test suite. CI runs it on every published base image (ADR 0008 §12):

    python -I -m unittest discover -s /path/to/images/helper
"""

import os
import stat
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import odooboat_helper as helper  # noqa: E402


def write(path, content, mode=0o644):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as handle:
        handle.write(content)
    os.chmod(path, mode)


class ReadSeries(unittest.TestCase):
    def tree(self, release):
        root = tempfile.mkdtemp()
        self.addCleanup(lambda: __import__("shutil").rmtree(root))
        write(os.path.join(root, "odoo", "release.py"), release)
        return root

    def test_series(self):
        self.assertEqual(helper.read_series(self.tree("version_info = (18, 0, 0, FINAL, 0, '')\n")), "18.0")
        self.assertEqual(helper.read_series(self.tree("version_info = ('saas~18', 1, 0, FINAL, 0, '')\n")), "saas~18.1")

    def test_comments_are_ignored(self):
        release = "# version_info = (6, 1, 0)\nversion_info = (17, 0, 0, FINAL, 0, '')\n"
        self.assertEqual(helper.read_series(self.tree(release)), "17.0")

    def test_missing_and_ambiguous(self):
        self.assertIsNone(helper.read_series(tempfile.gettempdir() + "/odooboat-no-such-tree"))
        with self.assertRaises(ValueError):
            helper.read_series(self.tree("version_info = (17, 0)\nversion_info = (18, 0)\n"))


class Check(unittest.TestCase):
    def setUp(self):
        self.bin = tempfile.mkdtemp()
        self.addCleanup(lambda: __import__("shutil").rmtree(self.bin))
        self.path = mock.patch.dict(os.environ, {"PATH": self.bin + os.pathsep + os.environ.get("PATH", "")})
        self.path.start()
        self.addCleanup(self.path.stop)

    def fake_pg_dump(self, version_line):
        write(os.path.join(self.bin, "pg_dump"), "#!/bin/sh\necho '%s'\n" % version_line, 0o755 | stat.S_IXUSR)

    def test_matching_client(self):
        self.fake_pg_dump("pg_dump (PostgreSQL) 16.4 (Debian 16.4-1.pgdg120+2)")
        report = helper.check("18.0", 16, community="/nonexistent")
        self.assertEqual(report["problems"], [])
        self.assertEqual(report["postgresClient"], 16)
        self.assertEqual(report["protocol"], helper.PROTOCOL)

    def test_client_mismatch(self):
        self.fake_pg_dump("pg_dump (PostgreSQL) 15.8")
        report = helper.check("18.0", 16, community="/nonexistent")
        self.assertIn("pg_dump is PostgreSQL 15, expected 16", report["problems"])

    def test_embedded_series_mismatch(self):
        self.fake_pg_dump("pg_dump (PostgreSQL) 16.4")
        root = tempfile.mkdtemp()
        self.addCleanup(lambda: __import__("shutil").rmtree(root))
        write(os.path.join(root, "odoo", "release.py"), "version_info = (17, 0, 0, FINAL, 0, '')\n")
        report = helper.check("18.0", 16, community=root)
        self.assertEqual(report["series"], "17.0")
        self.assertIn("community source is series 17.0, expected 18.0", report["problems"])

    def test_odoo_importable_outside_sources(self):
        self.fake_pg_dump("pg_dump (PostgreSQL) 16.4")
        with mock.patch.object(helper, "odoo_outside_sources", return_value="/venv/site-packages/odoo"):
            report = helper.check("18.0", 16, community="/nonexistent")
        self.assertTrue(any("importable" in p for p in report["problems"]))

    def test_missing_pg_dump(self):
        os.environ["PATH"] = self.bin
        report = helper.check("18.0", 16, community="/nonexistent")
        self.assertTrue(any(p.startswith("pg_dump:") for p in report["problems"]))


if __name__ == "__main__":
    unittest.main()
