package server

// User-facing strings the tests assert on, in one place.
//
// A test that asserts on a literal sentence asserts two things at once: that
// the state is reported at all, and that it is reported in those exact words.
// Only the first is a property worth protecting. Naming the sentence here lets
// the wording change without touching a test, while the test keeps meaning
// "this state is reported" — and it makes the set of things the UI says to an
// operator enumerable, which it was not when the sentences lived inline across
// six templates and three handlers.
//
// The Go-owned messages below are written into a view model by a handler. The
// template-owned ones are still spelled out in the templates; they are named
// here so the tests stop carrying the only copy, and the templates take them
// from here when they are rewritten over the shared layout.

// Go-owned: a handler assigns these.
const (
	// msgNotSaved heads a refused write. It says the write was refused BEFORE
	// listing why, because "nothing reached disk" is the part an operator needs
	// first — the individual problems are how to fix it, not what happened.
	msgNotSaved = "Not saved: the configuration would not load."
)

// savedFileMsg reports a landed write, naming the file rather than saying "ok".
// A tenant has two files and a deployment has three more; "Saved" alone leaves
// the operator to work out which one they were editing.
func savedFileMsg(tenant, kind string) string {
	return "Saved " + tenant + "." + kind + ".json"
}

// Template-owned: the templates spell these out today.
const (
	// msgProblemsHeading precedes the problems that blocked a write.
	msgProblemsHeading = "Nothing was saved."
	// msgWarningsHeading precedes findings that rode along with a success. It
	// must not read like a refusal; the file IS saved.
	msgWarningsHeading = "Saved, but worth a look:"
	// msgNoTenantHasFlows is the empty state when no tenant anywhere has a
	// graph — an explanation rather than a blank canvas.
	msgNoTenantHasFlows = "No tenant has a flow graph yet."
	// msgTenantDefinesNoFlows is the empty state for a tenant whose flows file
	// exists but declares nothing.
	msgTenantDefinesNoFlows = "defines no flows"
)
