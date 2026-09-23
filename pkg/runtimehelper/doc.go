// Package runtimehelper implements the node-artifact-runtime shim that runs
// inside a DAG node container: it materializes inputs, supervises one user
// command, and emits the output manifest.
//
// # Process-wide child reaping
//
// While Run is supervising the user command, runtimehelper owns child reaping
// for the whole process. On Linux it marks the process as a child subreaper so
// that orphaned descendants of the command are reparented to it, and it
// collects every exited child with wait4(-1). wait4(-1) cannot tell the
// command's direct child and adopted descendants apart from unrelated children
// of the same process, so the status of any child that Run did not start is
// discarded.
//
// Callers must therefore not start or wait for any other child process (for
// example through os/exec) while Run is active. Such a concurrent waiter can
// lose its child's exit status or fail with ECHILD. The node-artifact-runtime
// binary meets this precondition: it calls Run once per process and starts no
// other child processes.
package runtimehelper
