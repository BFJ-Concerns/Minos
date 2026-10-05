// A test fixture: serves the bare repositories under ROOT over Git's smart
// HTTP protocol through the real git http-backend, answering 401 to every
// request whose Basic authorisation is not USERNAME:PASSWORD. It prints its
// base URL on one line once listening and appends one JSON line per request
// — the path and whether it was authorised — to LOG.
//
//   node authenticated-git-server-fixture.mjs ROOT USERNAME PASSWORD LOG

import { spawn } from "node:child_process";
import { appendFileSync } from "node:fs";
import { createServer } from "node:http";

const [root, username, password, log] = process.argv.slice(2);
const expected = `Basic ${Buffer.from(`${username}:${password}`).toString("base64")}`;

const server = createServer((request, response) => {
  const authorised = request.headers.authorization === expected;
  appendFileSync(log, `${JSON.stringify({ url: request.url, authorised })}\n`);
  if (!authorised) {
    response.writeHead(401, { "WWW-Authenticate": 'Basic realm="fixture"' });
    response.end();
    return;
  }
  const url = new URL(request.url, "http://fixture");
  const backend = spawn("git", ["http-backend"], {
    env: {
      ...process.env, GIT_PROJECT_ROOT: root, GIT_HTTP_EXPORT_ALL: "1", REMOTE_USER: username,
      PATH_INFO: url.pathname, QUERY_STRING: url.search.slice(1), REQUEST_METHOD: request.method,
      CONTENT_TYPE: request.headers["content-type"] || "", GIT_PROTOCOL: request.headers["git-protocol"] || "",
      HTTP_CONTENT_ENCODING: request.headers["content-encoding"] || "",
      ...(request.headers["content-length"] ? { CONTENT_LENGTH: request.headers["content-length"] } : {}),
    },
    stdio: ["pipe", "pipe", "inherit"],
  });
  request.pipe(backend.stdin);
  // The backend answers as a CGI program: headers, a blank line, the body.
  let pending = Buffer.alloc(0);
  let headersSent = false;
  backend.stdout.on("data", (chunk) => {
    if (headersSent) {
      response.write(chunk);
      return;
    }
    pending = Buffer.concat([pending, chunk]);
    const end = pending.indexOf("\r\n\r\n");
    if (end < 0) return;
    let status = 200;
    const headers = {};
    for (const line of pending.subarray(0, end).toString("utf8").split("\r\n")) {
      const separator = line.indexOf(":");
      const name = line.slice(0, separator).trim();
      const value = line.slice(separator + 1).trim();
      if (name.toLowerCase() === "status") status = Number.parseInt(value, 10);
      else headers[name] = value;
    }
    response.writeHead(status, headers);
    headersSent = true;
    response.write(pending.subarray(end + 4));
  });
  backend.stdout.on("end", () => response.end());
});

server.listen(0, "127.0.0.1", () => {
  process.stdout.write(`http://127.0.0.1:${server.address().port}\n`);
});
