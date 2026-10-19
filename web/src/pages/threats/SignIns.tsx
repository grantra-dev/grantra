import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { Loaded } from "@/components/Loaded";
import { ShowMore } from "@/components/ShowMore";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Select } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useDebounced, usePages } from "@/lib/hooks";
import {
  signInsPageSize,
  listSignIns,
  type SignInEvent,
  type ThreatsOverview,
} from "@/lib/threats";
import { formatTime } from "@/lib/utils";

/** Whether the address box holds a whole address yet; the API refuses part of one. */
const wholeAddress = (s: string) =>
  /^\d{1,3}(\.\d{1,3}){3}$/.test(s) || /^[0-9a-f:]+:[0-9a-f]*$/i.test(s);

const outcomes: Record<SignInEvent["outcome"], ReactNode> = {
  success: <Badge variant="green">Signed in</Badge>,
  failure: <Badge variant="amber">Wrong password</Badge>,
  mfa_failure: <Badge variant="red">Second factor failed</Badge>,
};

/** Where sign-ins come from, and the latest ones. */
export function SignIns({ overview }: { overview?: ThreatsOverview }) {
  const [outcome, setOutcome] = useState("");
  const [ip, setIp] = useState("");
  const debounced = useDebounced(ip.trim());
  const typedIp = wholeAddress(debounced) ? debounced : "";
  const events = usePages(
    ["sign-ins", outcome, typedIp],
    (before?: number) => listSignIns({ outcome, ip: typedIp, before }),
    signInsPageSize,
  );
  const { rows } = events;
  return (
    <div className="flex flex-col gap-4">
      {overview && <Sources overview={overview} />}
      <div className="flex flex-wrap gap-3">
        <div>
          <Label htmlFor="signin-outcome">Outcome</Label>
          <Select id="signin-outcome" value={outcome} onChange={(e) => setOutcome(e.target.value)}>
            <option value="">Any</option>
            <option value="success">Signed in</option>
            <option value="failure">Wrong password</option>
            <option value="mfa_failure">Second factor failed</option>
          </Select>
        </div>
        <div>
          <Label htmlFor="signin-ip">Address</Label>
          <Input
            id="signin-ip"
            value={ip}
            onChange={(e) => setIp(e.target.value)}
            placeholder="203.0.113.7"
          />
        </div>
      </div>
      <Loaded
        loading={events.isPending}
        error={events.error}
        empty={rows.length === 0 && "No sign-in here yet."}
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>When</TableHead>
              <TableHead>Who</TableHead>
              <TableHead>Outcome</TableHead>
              <TableHead>From</TableHead>
              <TableHead>Where</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((e) => (
              <TableRow key={e.id} className="align-top text-sm">
                <TableCell className="whitespace-nowrap">{formatTime(e.occurred_at)}</TableCell>
                <TableCell>
                  {e.identity_id ? (
                    <Link
                      to={`/people/${e.identity_id}`}
                      className="text-brand-600 hover:underline"
                    >
                      {e.identity_name}
                    </Link>
                  ) : (
                    <span title="Nobody in Grantra has this username">{e.username ?? "–"}</span>
                  )}
                </TableCell>
                <TableCell>{outcomes[e.outcome]}</TableCell>
                <TableCell>
                  {e.ip ?? "–"}
                  {e.country && <span className="text-slate-500"> · {e.country}</span>}
                </TableCell>
                <TableCell className="text-slate-500">
                  {[e.source, e.application].filter(Boolean).join(" · ")}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        <ShowMore query={events} />
      </Loaded>
    </div>
  );
}

function Sources({ overview: o }: { overview: ThreatsOverview }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Where sign-ins come from</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        <p className="text-slate-600">
          {o.grantra_login ? (
            <>
              Grantra reads every sign-in to Grantra Login by itself, every minute (the{" "}
              {o.login_event_types.join(" and ")} events).
            </>
          ) : (
            <>
              Grantra Login is not connected, so Grantra only sees the sign-ins that scripts send.{" "}
              <Link to="/settings/grantra-login" className="text-brand-600 hover:underline">
                Set up Grantra Login
              </Link>
              .
            </>
          )}{" "}
          If people sign in with Entra ID, Okta or Google, a script can send their sign-ins to{" "}
          <code className="text-xs">POST /api/v1/sign-in-events</code> with a{" "}
          <Link to="/settings/api-tokens" className="text-brand-600 hover:underline">
            service token
          </Link>{" "}
          that may <code className="text-xs">{o.token_scope}</code>.
        </p>
        {o.sources.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Source</TableHead>
                <TableHead>Last 24 hours</TableHead>
                <TableHead>Matched to a person</TableHead>
                <TableHead>Latest</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {o.sources.map((s) => (
                <TableRow key={s.source}>
                  <TableCell className="font-medium">{s.source}</TableCell>
                  <TableCell>{s.last_day}</TableCell>
                  <TableCell>
                    {s.last_day === 0 ? "–" : `${Math.round((100 * s.identified) / s.last_day)}%`}
                  </TableCell>
                  <TableCell className="text-slate-500">
                    {s.last_at ? formatTime(s.last_at) : "–"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}
