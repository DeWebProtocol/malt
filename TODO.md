# TODO

## Follow-ups

- [ ] Fix `malt add --layout hierarchical` for top-level symlink directories.
  `collectAddInputs` follows symlink directories, but `buildAddStagingTree` routes them through `stageDirectoryInput`; `filepath.WalkDir` does not descend into the symlink root, so the add can create an empty directory with `files_imported=0`. Either special-case symlink directories in hierarchical layout or reject them explicitly, and add a regression test.