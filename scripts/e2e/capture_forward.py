#!/usr/bin/env python3
import http.server
import json
import os
import pathlib
import urllib.error
import urllib.request


class Handler(http.server.BaseHTTPRequestHandler):
    counter = 0

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        Handler.counter += 1
        event = self.headers.get("X-Forgejo-Event", "unknown")
        action = "unknown"
        try:
            action = json.loads(body.decode("utf-8")).get("action") or "none"
        except Exception:
            pass
        fixture_dir = pathlib.Path(os.environ["MINOS_FIXTURE_DIR"])
        fixture_dir.mkdir(parents=True, exist_ok=True)
        name = f"{Handler.counter:03d}-{event}-{action}.json"
        (fixture_dir / name).write_text(json.dumps({
            "headers": {k: v for k, v in self.headers.items()},
            "body": body.decode("utf-8"),
        }, indent=2, sort_keys=True) + "\n")

        forward = os.environ.get("MINOS_FORWARD_URL")
        status = 202
        response_body = b"captured\n"
        if forward:
            request = urllib.request.Request(
                forward,
                data=body,
                method="POST",
                headers={k: v for k, v in self.headers.items() if k.lower() != "host"},
            )
            try:
                with urllib.request.urlopen(request, timeout=30) as response:
                    status = response.status
            except urllib.error.HTTPError as err:
                status = err.code
            except Exception:
                # A non-HTTP failure means the receiver did not accept the
                # delivery. Reporting success here would make the e2e lie.
                status = 502
                response_body = b"forward failed\n"
        self.send_response(status)
        self.end_headers()
        self.wfile.write(response_body)

    def log_message(self, fmt, *args):
        return


if __name__ == "__main__":
    port = int(os.environ["MINOS_CAPTURE_PORT"])
    http.server.ThreadingHTTPServer(("0.0.0.0", port), Handler).serve_forever()
