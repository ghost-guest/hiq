import logoWordmark from "../assets/welcome-hero.png";
import { useT } from "../lib/i18n";

// Welcome is the empty-state landing: a one-liner, the input affordances
// (/ commands, @ files, Enter), and a few clickable example prompts.
//
// Two profiles differ here:
//   - dev: the 4 welcome.ex1-4 prompts send immediately (one click → one turn),
//     via onPrompt. This is the original behavior.
//   - cowork: 6 office-oriented "starter" bubbles that FILL the composer
//     instead of sending (onInsert), so the user can tweak the prompt first.
//     These bind to cowork capabilities (weekly report, spreadsheet, mind map,
//     expert-team review) plus 2 generic ones (explain code, translate).
//
// profile + onInsert are optional props: Welcome renders the dev layout when
// profile is absent/"dev", preserving the old single-prop contract.

export function Welcome({
  onPrompt,
  profile,
  onInsert,
  onRemoteConnect,
}: {
  onPrompt: (text: string) => void;
  profile?: "dev" | "cowork" | "netdev";
  onInsert?: (text: string) => void;
  onRemoteConnect?: () => void;
}) {
  const t = useT();

  // Dev profile: the classic 4 immediate-send examples.
  const devExamples = [t("welcome.ex1"), t("welcome.ex2"), t("welcome.ex3"), t("welcome.ex4")];

  // Cowork profile: 6 office starter bubbles (4 office + 2 generic). Each
  // references a real cowork capability so a first-time user discovers what the
  // office mode can do. ex4 (expert-team review) is backed by the
  // expert_team_run tool registered under cowork.
  const coworkExamples = [
    t("welcome.coworkEx1"),
    t("welcome.coworkEx2"),
    t("welcome.coworkEx3"),
    t("welcome.coworkEx4"),
    t("welcome.coworkEx5"),
    t("welcome.coworkEx6"),
  ];

  // Netdev profile: ops-oriented starters. Like cowork they FILL the composer
  // (onInsert) instead of sending — ops prompts name devices/paths the user
  // must first adapt to their own inventory, and an immediate send would
  // dead-end on an empty inventory.
  const netdevExamples = [
    t("welcome.netdevEx1"),
    t("welcome.netdevEx2"),
    t("welcome.netdevEx3"),
    t("welcome.netdevEx4"),
    t("welcome.netdevEx5"),
    t("welcome.netdevEx6"),
  ];

  const isCowork = profile === "cowork";
  const isNetdev = profile === "netdev";

  const examples = isNetdev ? netdevExamples : isCowork ? coworkExamples : devExamples;
  // Cowork/netdev bubbles fill the composer (editable before send); dev
  // bubbles send immediately (the original fast-start behavior). Fall back to
  // onPrompt if the caller forgot to wire onInsert, so a click never dead-ends.
  const handlePick = (text: string) => {
    if ((isCowork || isNetdev) && onInsert) {
      onInsert(text);
    } else {
      onPrompt(text);
    }
  };

  const title = isNetdev ? t("welcome.netdevTitle") : t("welcome.title");
  const tagline = isNetdev ? t("welcome.netdevTagline") : t("welcome.tagline");

  return (
    <div className={`welcome welcome--brand${isCowork ? " welcome--cowork" : ""}${isNetdev ? " welcome--netdev" : ""}`}>
      <span className="welcome__brand">
        <img src={logoWordmark} className="welcome__brand-logo" alt="Hiq" draggable={false} />
      </span>
      <h2 className="welcome__title">{title}</h2>
      <div className="welcome__tag">
        {tagline}
      </div>

      <div className="welcome__hints">
        <span>
          <kbd>/</kbd> {t("welcome.hintCommands")}
        </span>
        <span>
          <kbd>@</kbd> {t("welcome.hintFiles")}
        </span>
        <span>
          <kbd>⏎</kbd> {t("welcome.hintSend")}
        </span>
      </div>

      <div className="welcome__examples">
        {examples.map((ex) => (
          <button key={ex} className="welcome__ex" onClick={() => handlePick(ex)}>
            {ex}
          </button>
        ))}
      </div>

      {onRemoteConnect && (
        <button className="welcome__remote" onClick={onRemoteConnect}>
          {t("welcome.remoteConnect")}
        </button>
      )}
    </div>
  );
}
