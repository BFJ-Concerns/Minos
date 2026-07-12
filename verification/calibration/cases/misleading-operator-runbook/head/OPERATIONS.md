# Failure recovery

Infrastructure failures use a fixed retry ladder.

After two failed attempts, the service posts the backend error on the pull
request and stops retrying until a maintainer re-arms it.
