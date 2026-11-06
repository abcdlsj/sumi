import { useMutation, useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { Fingerprint, LoaderCircle, MonitorCheck } from "lucide-react";
import { useState, type FormEvent } from "react";

import { confirmPairing, getPairingDetails, listSpaces } from "../api/client";
import { SumiLogo } from "../components/SumiLogo";
import { SumiSelect } from "../components/SumiSelect";
import { OnboardingSkeleton } from "../components/Skeleton";

export function PairComputerPage() {
  const params = useParams({ strict: false });
  const pairingId = String(params.pairingId);
  const search = useSearch({ strict: false });
  const code = typeof search.code === "string" ? search.code : typeof search.code === "number" ? String(search.code) : "";
  const navigate = useNavigate();
  const details = useQuery({ queryKey: ["computer-pairing", pairingId, code], queryFn: () => getPairingDetails(pairingId, code), retry: false, enabled: Boolean(code) });
  const spaces = useQuery({ queryKey: ["spaces"], queryFn: listSpaces, retry: false });
  const [chosenSpaceId, setChosenSpaceId] = useState("");
  // One Space needs no choice: the pairing confirms into it directly.
  const spaceId = chosenSpaceId || (spaces.data?.length === 1 ? spaces.data[0].id : "");
  const confirmation = useMutation({
    mutationFn: (input: { space_id: string; name: string; code: string }) => confirmPairing(pairingId, input),
    onSuccess: (computer) => {
      const space = spaces.data?.find((candidate) => candidate.id === computer.space_id);
      // Back to #general, where the setup guide picks up at "Create your first Agent".
      if (space) void navigate({ to: "/s/$spaceSlug/channels/$channelSlug", params: { spaceSlug: space.slug, channelSlug: "general" } });
    },
  });

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    confirmation.mutate({ space_id: String(form.get("space_id") ?? ""), name: String(form.get("name") ?? ""), code });
  }

  if (!code) return <div className="route-status route-status--error">The pairing code is missing from this URL.</div>;
  if (details.isPending || spaces.isPending) return <OnboardingSkeleton label="Checking Computer" />;
  if (details.error || spaces.error || !details.data) return <div className="route-status route-status--error">{details.error?.message ?? spaces.error?.message ?? "Pairing unavailable."}</div>;

  return (
    <main className="onboarding-shell">
      <header className="brand-bar">
        <Link className="wordmark" to="/" search={{ redirect: undefined }} aria-label="Sumi home"><span className="wordmark-mark"><SumiLogo /></span>Sumi</Link>
        <span className="step-index">Pair Computer</span>
      </header>
      <section className="pairing-stage" aria-labelledby="pairing-title">
        <div className="pairing-machine">
          <MonitorCheck aria-hidden="true" />
          <h1 id="pairing-title">{details.data.hostname}</h1>
          <dl>
            <div><dt>Operating system</dt><dd>{details.data.os}</dd></div>
            <div><dt>Daemon</dt><dd>v{details.data.daemon_version}</dd></div>
            <div className="fingerprint"><dt><Fingerprint /> Computer Token fingerprint</dt><dd>{details.data.token_fingerprint}</dd></div>
          </dl>
        </div>
        <form className="pairing-form" onSubmit={submit}>
          <label>Space<input type="hidden" name="space_id" value={spaceId} /><SumiSelect ariaLabel="Space" value={spaceId} onChange={setChosenSpaceId} options={(spaces.data ?? []).map((space) => ({ value: space.id, label: `${space.name} /s/${space.slug}` }))} /></label>
          <label htmlFor="computer-name">Computer name</label>
          <input id="computer-name" name="name" required maxLength={80} defaultValue={details.data.hostname} />
          <button className="primary-action" type="submit" disabled={confirmation.isPending || !code}>
            {confirmation.isPending ? "Pairing" : "Pair Computer"}
            {confirmation.isPending ? <LoaderCircle className="spin" /> : <MonitorCheck />}
          </button>
          {!code ? <p className="form-error">The pairing code is missing from this URL.</p> : null}
          {confirmation.error ? <p className="form-error" role="alert">{confirmation.error.message}</p> : null}
        </form>
      </section>
    </main>
  );
}
