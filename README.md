1. used proto to generate compatible ployglot code
2. using buf generated the code
3. install go packages
4. set up python venv
5. start python server
6. try assist


Every edit says which revision it was made against (base_revision). The server accepts it only if that is the current revision. If someone else got there first, it rejects the edit with ABORTED and changes nothing. The client catches up, rebases its edit and resends it. In the browser, CodeMirror's collab package will do that rebasing.
Each edit has a unique op_id, made of a client ID and a sequence number. A resent edit that was already applied is counted as a duplicate and skipped, not applied twice.
All decisions happen in the state machine (Apply), which never reads the clock or uses random numbers. A test feeds two copies of it the same 300 random batches (including stale, duplicate and invalid ones) and checks that they end up identical. That is the property Raft depends on in Milestone 2.
Writes go through an oplog.Log interface. Today LocalLog applies edits immediately. In Milestone 2 a Raft log takes its place, and nothing else changes.