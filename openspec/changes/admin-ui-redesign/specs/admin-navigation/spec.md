## ADDED Requirements

### Requirement: One shell serves every admin page

The UI server SHALL render every admin page through a single layout providing the header, the sidebar and the content region. No page SHALL carry its own copy of that shell.

#### Scenario: The dashboard and an admin page share a sidebar

- **WHEN** `/` and `/admin/tenants` are both rendered
- **THEN** the header and sidebar markup is identical between them
- **AND** it is defined in exactly one template file

#### Scenario: A partial has one definition

- **WHEN** the dashboard renders its first paint of the sessions list and the sessions poll returns
- **THEN** both come from the same template definition
- **AND** no second copy of that markup exists in the tree

### Requirement: Every navigation entry is a real URL

Each sidebar entry SHALL be a URL that renders a complete page on a hard GET, is bookmarkable, and survives a refresh. Navigation between pages SHALL be a browser navigation rather than a fragment swap.

#### Scenario: Every route renders standalone

- **WHEN** any of `/`, `/admin/tenants`, `/admin/deployment`, `/admin/audio`, `/admin/live/registrations`, `/admin/live/dialogs`, `/admin/live/sessions`, `/admin/live/parked` or `/admin/live/rtpmanagers` is requested directly
- **THEN** the response is a complete HTML page with the shell around it
- **AND** requesting it again after a refresh renders the same page

#### Scenario: A page and its fragment are different URLs

- **WHEN** `/admin/live/dialogs` and `/admin/partials/dialogs` are both requested
- **THEN** the first returns a full page and the second returns only the fragment
- **AND** both were built from the same view model

#### Scenario: Flows are not a top-level destination

- **WHEN** the sidebar is rendered
- **THEN** it offers Overview, Tenants, Deployment, Audio and the Live monitors
- **AND** it offers no flows entry, because a flow belongs to a tenant

#### Scenario: Existing deep links keep working

- **WHEN** a bookmarked `/admin/config?tab=flows&tenant=devtenant` is requested
- **THEN** the server redirects to the equivalent new URL
- **AND** does not answer 404

### Requirement: Backend selection survives every navigation

Where more than one signaling backend is configured, the selected backend SHALL be carried on every internal link and every fragment request, and SHALL be resolved to a canonical name by the server rather than echoed from the query string.

#### Scenario: The selection follows a link

- **WHEN** an operator selects a backend and then navigates to any other admin page
- **THEN** that page is rendered against the same backend
- **AND** its own links carry the selection onward

#### Scenario: No rendered link drops the selection

- **WHEN** any admin page is rendered with a backend selected
- **THEN** every internal link and every `hx-get`/`hx-post` URL in the response carries the backend parameter

#### Scenario: An unknown backend name is not echoed

- **WHEN** a page is requested with a backend name that is not configured
- **THEN** the page renders against the default backend
- **AND** the name it reports is the configured one, not the requested one

### Requirement: A page fetches only what it renders

Building a page's data SHALL request from each backend only the data that page displays.

#### Scenario: A monitor poll is scoped

- **WHEN** the dialogs partial is polled
- **THEN** the server requests dialogs and health from each backend
- **AND** does not request registrations, sessions, parked calls or RTP managers

### Requirement: The dashboard is an overview

`/` SHALL present operational state rather than a scroll target for every table: backend health, RTP managers with drain state, active sessions against channel limits, and the tenants with their warning state.

#### Scenario: Configuration drift is on the front page

- **WHEN** a tenant file has been written since the routing cache was built
- **THEN** the dashboard says so prominently, naming what is not yet in force
- **AND** offers the reload action next to it

#### Scenario: Drift distinguishes reloadable from restart-only

- **WHEN** a deployment-wide file has been written since the process started
- **THEN** the dashboard reports it separately from stale tenants
- **AND** says that only a restart activates it

#### Scenario: A tenant is one click from the dashboard

- **WHEN** the dashboard lists a tenant
- **THEN** it shows that tenant's validation warning state
- **AND** links to that tenant's workspace

#### Scenario: The monitors are no longer stacked on the front page

- **WHEN** the dashboard is rendered
- **THEN** registrations, dialogs, sessions and parked calls are not all listed on it
- **AND** each is reachable at its own Live route

### Requirement: In-page behaviour is preserved

Refreshing behaviour that exists today SHALL survive the restructure unchanged, including polling intervals and the suspension of RTP manager polling while a drain modal is open.

#### Scenario: Polling continues at the current cadence

- **WHEN** a Live page is open
- **THEN** its partial polls at the interval it uses today
- **AND** swaps in place without navigating

#### Scenario: The drain modal still freezes the table under it

- **WHEN** an operator opens the drain confirmation for an RTP manager
- **THEN** the RTP manager list stops refreshing until the modal closes
- **AND** resumes afterwards
