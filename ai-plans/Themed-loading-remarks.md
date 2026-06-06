Themed loading remarks feature

New loading_remarks_themes.go with vault-boy, knight-rider, skein theme strings + a lookup function
Add LoadingTheme string to ReqContextData, read it from the X-Loading-Theme request header in FetchContext
Thread the theme through newLoadingWriter → loadingWriter.start()
Update base.go and all test callsites


GET /loading-remark?theme=pip-boy


var loadingRemarksPipBoy = []string{
	"Please stand by",
	"Vault-Tec approved model initialization in progress",
	"Loading your complimentary post-apocalyptic autocomplete",
	"Rebuilding civilization, one token at a time",
	"Polishing the Pip-Boy while the weights unpack",
	"Please remain calm. The overseer has been notified",
	"Radscorpion-free inference is not guaranteed",
	"Calibrating the Geiger counter for hallucinations",
	"Reticulating vault splines",
	"Allocating bottle caps for GPU time",
	"Teaching the model to say 'war never changes' with confidence",
	"Rehydrating pre-war knowledge from compressed rations",
	"Checking if the model has acquired any new mutations",
	"Installing charisma module. Results may vary",
	"Loading SPECIAL stats: Strength 1, Intelligence maybe",
	"Your prompt has entered the wasteland",
	"Purifying dirty tokens through the water chip",
	"Please enjoy this educational Vault-Tec waiting period",
	"Preparing a friendly thumbs-up while the GPU suffers",
	"Consulting the terminal marked DO NOT TOUCH",
	"Spinning up the reactor. Ignore the clicking sound",
	"Reconstructing the old world from suspicious training data",
	"Equipping power armor for heavy matrix multiplication",
	"Trading latency for two stimpaks and a desk fan",
	"Vault door opening very slowly for dramatic effect",
}
var loadingRemarksKnightRider = []string{
	"Initializing KITT",
	"Scanning for hostiles, roadblocks, and bad prompts",
	"Turbo Boost unavailable during model load",
	"Knight Industries neural network coming online",
	"Michael, I recommend patience",
	"Activating red scanner sweep",
	"Analyzing the situation with 1980s confidence",
	"Engaging silent mode. The GPU fan disagrees",
	"Loading molecular bonded shell. Mostly metaphorical",
	"Running diagnostics on the sarcastic response module",
	"Preparing pursuit mode for incoming tokens",
	"Synchronizing dashboard lights with excessive drama",
	"Accessing Knight Industries database",
	"The model is not just a car, it is a very expensive autocomplete",
	"Plotting optimal route through latent space",
	"Michael, this may take a moment",
	"Calibrating voice synthesizer for maximum smugness",
	"Engaging auto-cruise through matrix multiplication",
	"Detecting unusually high levels of David Hasselhoff energy",
	"Recharging the turbo boost capacitor",
	"Please do not question the talking computer",
	"Running crime-fighting heuristics against your prompt",
	"Polishing the black paint while weights load",
	"Loading pursuit mode, sass mode, and token mode",
	"Stand by. KITT is thinking very dramatically",
}

For Skein,

var loadingRemarksSkein = []string{
	"Untangling the skein",
	"Threading tokens through the loom",
	"Weaving context into something suspiciously coherent",
	"Pulling one more thread from the latent spool",
	"Spinning wool into logits",
	"Knitting the prompt into a response-shaped object",
	"Following the thread through the maze",
	"Braiding goroutines into useful behavior",
	"Carding raw chaos into structured output",
	"Finding the loose end. There is always a loose end",
	"Rewinding the spool after an unfortunate async incident",
	"Twisting fibers of probability into text",
	"The loom is humming. The GPU is screaming",
	"Measuring twice, generating once",
	"Loading the pattern from memory. Memory objects to this",
	"Tracing the golden thread through the call graph",
	"Combing knots out of the context window",
	"Preparing a tapestry of questionable certainty",
	"Spooling up the model. Literally this time",
	"Binding tools, prompts, and regrets",
	"Weaving warp and weft across the attention heads",
	"Consulting the Norns of inference",
	"Fate is compiling. Please hold",
	"Pulling destiny through a JSON schema",
	"Aligning the threads before the first token snaps",
}

API shape ideas.

type LoadingRemarkTheme string

const (
	LoadingRemarkThemeDefault     LoadingRemarkTheme = "default"
	LoadingRemarkThemeVaultBoy    LoadingRemarkTheme = "vault-boy"
	LoadingRemarkThemeKnightRider LoadingRemarkTheme = "knight-rider"
	LoadingRemarkThemeSkein       LoadingRemarkTheme = "skein"
)

var loadingRemarkThemes = map[LoadingRemarkTheme][]string{
	LoadingRemarkThemeDefault:     loadingRemarks,
	LoadingRemarkThemeVaultBoy:    loadingRemarksVaultBoy,
	LoadingRemarkThemeKnightRider: loadingRemarksKnightRider,
	LoadingRemarkThemeSkein:       loadingRemarksSkein,
}