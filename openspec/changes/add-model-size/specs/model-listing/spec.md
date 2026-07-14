# Spec delta: model-listing (add-model-size)

## ADDED

### Model size in /v1/models

- Each local model object in `GET /v1/models` gains a `size_bytes` (int64)
  field: the on-disk size of the model weights — the GGUF file size for
  llama.cpp, the summed safetensors size for MLX. It is omitted when the size
  cannot be determined (peer models, an unresolvable weight path, or a backend
  whose weights are not modeled). Clients use it to show a human-readable size
  (e.g. GB) when choosing between similar quantizations of the same model.
- The value is stable for a given weight file; the server MAY memoize it and
  refresh on config reload.
