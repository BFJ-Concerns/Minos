# Failure recovery

Infrastructure failures are retried with deployment-configured backoff.
Backend diagnostics remain in the operator incident and service logs; the pull
request shows only the current product state.
