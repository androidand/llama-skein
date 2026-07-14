# Spec delta: model-routing (bound-swap-queue-wait)

## ADDED

### Bounded wait behind a busy sibling model

- When a request for one model must wait because loading it would require
  evicting another model that is still handling requests (or colliding with
  an in-flight swap), the router MUST bound that wait via
  `swapQueueTimeoutSecs` (default 10 seconds; 0 disables the bound).
- If the blocking model does not become available within that bound, the
  router MUST refuse the waiting request with a clear error (`503`,
  `ErrSwapQueueTimeout`) naming the model(s) it was waiting on, rather than
  continuing to wait with no upper limit.
- This bound MUST NOT change the eviction decision itself: a model with an
  in-flight request is still never interrupted to satisfy a different
  request. The bound only applies to how long the competing request is
  willing to wait for that model to go idle on its own.
