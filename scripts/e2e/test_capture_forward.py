#!/usr/bin/env python3
import http.server
import importlib.util
import os
import pathlib
import socket
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from unittest import mock


SCRIPT = pathlib.Path(__file__).with_name("capture_forward.py")
SPEC = importlib.util.spec_from_file_location("capture_forward", SCRIPT)
capture_forward = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(capture_forward)


class CaptureForwardTest(unittest.TestCase):
    def test_transport_failure_is_not_reported_as_accepted(self):
        with tempfile.TemporaryDirectory() as fixture_dir:
            # Keep the port reserved without listening so the forward attempt
            # is deterministically refused and cannot be claimed by the server.
            unavailable = socket.socket()
            unavailable.bind(("127.0.0.1", 0))
            unavailable_port = unavailable.getsockname()[1]

            server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), capture_forward.Handler)
            thread = threading.Thread(target=server.serve_forever)
            thread.start()
            try:
                environment = {
                    "PUMP19_FIXTURE_DIR": fixture_dir,
                    "PUMP19_FORWARD_URL": f"http://127.0.0.1:{unavailable_port}/hooks/local",
                }
                request = urllib.request.Request(
                    f"http://127.0.0.1:{server.server_address[1]}/hooks/local",
                    data=b'{}',
                    method="POST",
                    headers={"Content-Type": "application/json"},
                )
                with mock.patch.dict(os.environ, environment, clear=False):
                    with self.assertRaises(urllib.error.HTTPError) as raised:
                        urllib.request.urlopen(request)
                try:
                    self.assertEqual(raised.exception.code, 502)
                finally:
                    raised.exception.close()
            finally:
                server.shutdown()
                server.server_close()
                thread.join()
                unavailable.close()


if __name__ == "__main__":
    unittest.main()
