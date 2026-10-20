package settings

// Keys of the settings, for the packages that read them.
const (
	RequestStepTimeoutHours = "requests.step_timeout_hours"
	RequestReminderHours    = "requests.reminder_hours"
	RequestTeamMinPeople    = "requests.team_min_people"

	ReviewDefaultDueDays = "reviews.default_due_days"
	ReviewReminderHours  = "reviews.reminder_hours"

	SoDExceptionMaxDays     = "sod.exception_max_days"
	SoDExceptionsWithoutEnd = "sod.exceptions_without_end"

	BreakGlassMinutes       = "vault.break_glass_minutes"
	BreakGlassReminderHours = "vault.break_glass_reminder_hours"
	PasswordLength          = "vault.password_length"

	RequireMFADays      = "itdr.require_mfa_days"
	SignInRetentionDays = "itdr.sign_in_retention_days"

	UnusedDays          = "insights.unused_days"
	RoleMinPeople       = "insights.role_min_people"
	RoleMinSharePercent = "insights.role_min_share_percent"
	RoleMinEntitlements = "insights.role_min_entitlements"
	CommonMinPeers      = "insights.common_min_peers"
	RareMinPeers        = "insights.rare_min_peers"
	NHIExpiringDays     = "nhi.expiring_days"

	SessionIdleMinutes = "console.session_idle_minutes"
	SessionMaxHours    = "console.session_max_hours"
)

// Groups of settings, in the order the console shows them.
const (
	GroupRequests = "requests"
	GroupReviews  = "reviews"
	GroupSoD      = "sod"
	GroupPAM      = "pam"
	GroupThreats  = "threats"
	GroupInsights = "insights"
	GroupConsole  = "console"
)

// definitions is every setting. Defaults are what Grantra did before the
// setting existed, so a tenant that changes nothing sees no difference.
var definitions = []Definition{
	{Key: RequestStepTimeoutHours, Group: GroupRequests, Kind: Number, Unit: "hours", Default: 7 * 24, Min: 24, Max: 30 * 24,
		Title: "Time to decide each step",
		Help:  "How long each approver has to decide. A request nobody decides in time expires, and the person can ask again."},
	{Key: RequestReminderHours, Group: GroupRequests, Kind: Number, Unit: "hours", Default: 24, Min: 1, Max: 7 * 24,
		Title: "Remind approvers before a step expires",
		Help:  "Approvers who have not decided get one reminder this long before their step expires."},
	{Key: RequestTeamMinPeople, Group: GroupRequests, Kind: Number, Unit: "people", Default: 3, Min: 2, Max: 50, Looser: Lower,
		Title: "Smallest team whose access is counted",
		Help:  "Request access shows how many teammates hold each item, to help people pick. In smaller teams a count would tell what a given colleague holds, so it is not shown."},

	{Key: ReviewDefaultDueDays, Group: GroupReviews, Kind: Number, Unit: "days", Default: 14, Min: 1, Max: 90,
		Title: "Time reviewers get by default",
		Help:  "What a new campaign proposes for how long reviewers have. Whoever launches it can still pick another length."},
	{Key: ReviewReminderHours, Group: GroupReviews, Kind: Number, Unit: "hours", Default: 48, Min: 1, Max: 14 * 24,
		Title: "Remind reviewers before a campaign ends",
		Help:  "Reviewers with access still to review get one reminder this long before the campaign ends."},

	{Key: SoDExceptionMaxDays, Group: GroupSoD, Kind: Number, Unit: "days", Default: 365, Min: 1, Max: 365, Looser: Higher,
		Title: "Longest exception",
		Help:  "The longest an admin may accept a separation of duties risk for, on a request or on a violation."},
	{Key: SoDExceptionsWithoutEnd, Group: GroupSoD, Kind: Switch, Default: 1, Min: 0, Max: 1, Looser: Higher,
		Title: "Allow exceptions with no end date",
		Help:  "Whether an admin may accept a violation for good. Turned off, every exception needs an end date. Risks accepted on a request always have one."},

	{Key: BreakGlassMinutes, Group: GroupPAM, Module: "pam", Kind: Number, Unit: "minutes", Default: 60, Min: 15, Max: 8 * 60, Looser: Higher,
		Title: "Longest break-glass check-out",
		Help:  "A break-glass check-out lasts the account's check-out time, but never longer than this. Its password changes when it ends."},
	{Key: BreakGlassReminderHours, Group: GroupPAM, Module: "pam", Kind: Number, Unit: "hours", Default: 24, Min: 1, Max: 7 * 24,
		Title: "Remind admins of break-glass uses to review",
		Help:  "How often admins hear about break-glass uses nobody has reviewed yet."},
	{Key: PasswordLength, Group: GroupPAM, Module: "pam", Kind: Number, Unit: "characters", Default: 24, Min: 16, Max: 128, Looser: Lower,
		Title: "Length of generated passwords",
		Help:  "How long the passwords the vault sets when it rotates an account are. Check what the applications accept before making them longer."},

	{Key: RequireMFADays, Group: GroupThreats, Module: "itdr", Kind: Number, Unit: "days", Default: 7, Min: 1, Max: 90, Looser: Lower,
		Title: "How long \"Require MFA\" lasts",
		Help:  "How long someone must use a second factor after the \"Require MFA\" response, when whoever responds does not say."},
	{Key: SignInRetentionDays, Group: GroupThreats, Module: "itdr", Kind: Number, Unit: "days", Default: 400, Min: 366, Max: 5 * 365, Looser: Lower,
		Title: "Keep sign-ins for",
		Help:  "Sign-ins older than this are deleted. At least a year, so detections that look back up to a year (dormant accounts, new countries) keep working; investigations and audits often want more."},

	{Key: UnusedDays, Group: GroupInsights, Kind: Number, Unit: "days", Default: 90, Min: 30, Max: 730, Public: true,
		Title: "Unused after",
		Help:  "Access nobody used for this long, and held at least this long, counts as unused, in insights, review suggestions and non-human identities."},
	{Key: RoleMinPeople, Group: GroupInsights, Kind: Number, Unit: "people", Default: 5, Min: 2, Max: 100,
		Title: "Smallest group for a suggested role",
		Help:  "Role suggestions look at departments and job titles with at least this many people."},
	{Key: RoleMinSharePercent, Group: GroupInsights, Kind: Number, Unit: "percent", Default: 80, Min: 50, Max: 100,
		Title: "Share of a group that must hold it",
		Help:  "An entitlement goes into a group's suggested role when at least this share of the group holds it."},
	{Key: RoleMinEntitlements, Group: GroupInsights, Kind: Number, Unit: "entitlements", Default: 2, Min: 2, Max: 20,
		Title: "Fewest entitlements in a suggested role",
		Help:  "A suggested role needs at least this many entitlements to be worth creating."},
	{Key: CommonMinPeers, Group: GroupInsights, Kind: Number, Unit: "people", Default: 3, Min: 2, Max: 50,
		Title: "Peers needed to call access common",
		Help:  "Review suggestions call access common among peers (same manager or department) only in groups at least this big."},
	{Key: RareMinPeers, Group: GroupInsights, Kind: Number, Unit: "people", Default: 5, Min: 3, Max: 100,
		Title: "Peers needed to call access rare",
		Help:  "Review suggestions call access rare among peers only in groups at least this big."},
	{Key: NHIExpiringDays, Group: GroupInsights, Kind: Number, Unit: "days", Default: 30, Min: 1, Max: 180,
		Title: "Non-human identities expiring soon",
		Help:  "A service account, agent or workload whose end date is this close is flagged as expiring."},

	{Key: SessionIdleMinutes, Group: GroupConsole, Kind: Number, Unit: "minutes", Default: 30, Min: 5, Max: 8 * 60, Looser: Higher,
		Title: "Sign out after inactivity",
		Help:  "People are signed out of the console after this long without using it."},
	{Key: SessionMaxHours, Group: GroupConsole, Kind: Number, Unit: "hours", Default: 8, Min: 1, Max: 24, Looser: Higher,
		Title: "Longest console session",
		Help:  "People sign in again after this long, active or not."},
}
