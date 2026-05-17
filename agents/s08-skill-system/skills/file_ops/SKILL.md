# file_ops

> Read and write files within the workspace.

## When to Use

- The user asks you to inspect a config or source file.
- You need to draft a new file based on what is on disk.
- You need to overwrite an existing file (parent directories will be auto-created).

## Available Tools

- `read_file(path)` — read a UTF-8 file and return its full contents.
- `write_file(path, content)` — create or overwrite a file. Parent dirs are created.

## Conventions

- Prefer absolute paths. Relative paths resolve against the workspace root.
- Always `read_file` before overwriting a file you did not just create — assume the file matters.
- The `content` argument is verbatim; include trailing newlines if you want them in the output.

## Example

To replace a sentinel in `config.yaml`:

1. `read_file("/workspace/config.yaml")` → review current contents.
2. Build the new body with the sentinel replaced.
3. `write_file("/workspace/config.yaml", "<new body>")`.
