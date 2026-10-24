import { useState } from "react";

import { describe } from "../api/client";
import {
  useHandleParentReport,
  useParentCodes,
  useParentReports,
  type ParentReport,
} from "../api/hooks";
import {
  Empty,
  ErrorNotice,
  Loading,
  Notice,
  OfficeSelect,
  useOfficeParam,
} from "../components/ui";
import { dateTime } from "../lib/dates";

const LANG = { uz: "Uzbek", ru: "Russian" } as const;

// Parents follow a child in the app with the child's 5-digit code (no
// accounts). Here: what they report, and the codes, for a parent who asks.
export function ParentsPage() {
  const [officeId, setOfficeId] = useOfficeParam();
  const [status, setStatus] = useState<"open" | "handled">("open");
  const reports = useParentReports(status, officeId);

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>Parents</h1>
          <p className="muted">
            Parents follow their child in the app with the child’s 5-digit
            parent code: classes, schedule and attendance, read only. Students
            find their code in the app’s Profile.
          </p>
        </div>
        <div className="toolbar">
          <OfficeSelect value={officeId} onChange={setOfficeId} />
        </div>
      </div>

      <div className="toolbar" role="group" aria-label="Show reports">
        <button
          className={`chip plain ${status === "open" ? "selected" : ""}`}
          onClick={() => setStatus("open")}
        >
          To answer
        </button>
        <button
          className={`chip plain ${status === "handled" ? "selected" : ""}`}
          onClick={() => setStatus("handled")}
        >
          Handled
        </button>
      </div>

      {reports.isPending ? (
        <Loading />
      ) : reports.isError ? (
        <ErrorNotice error={reports.error} title="Couldn’t load the reports" />
      ) : reports.data.reports.length === 0 ? (
        <Empty>
          {status === "open"
            ? "No reports to answer. 🎉"
            : "No handled reports yet."}
        </Empty>
      ) : (
        reports.data.reports.map((r) => <ReportCard key={r.id} report={r} />)
      )}

      <CodeLookup officeId={officeId} />
    </div>
  );
}

function ReportCard({ report: r }: { report: ParentReport }) {
  const [reply, setReply] = useState("");
  const handle = useHandleParentReport();
  return (
    <div className="card stack" style={{ gap: 10 }}>
      <div className="card-head" style={{ marginBottom: 0 }}>
        <div>
          <strong>
            {r.last_name} {r.first_name}
          </strong>
          {r.group_name && <span className="muted"> · {r.group_name}</span>}
          <div className="small faint">
            {dateTime(r.created_at)} · written in {LANG[r.lang]}
          </div>
        </div>
        {r.handled_at ? (
          <span className="chip success">Handled</span>
        ) : (
          <span className="chip warning">To answer</span>
        )}
      </div>
      <p style={{ whiteSpace: "pre-wrap", margin: 0 }}>{r.message}</p>
      {r.handled_at ? (
        <div className="small muted">
          Handled by {r.handled_by} · {dateTime(r.handled_at)}
          {r.reply && (
            <div style={{ whiteSpace: "pre-wrap", marginTop: 4 }}>
              Answer: {r.reply}
            </div>
          )}
        </div>
      ) : (
        <div className="stack" style={{ gap: 8 }}>
          <label className="field">
            <span className="small muted">
              Answer the parent sees in the app (optional, in their language)
            </span>
            <textarea
              className="textarea"
              rows={2}
              maxLength={1000}
              value={reply}
              onChange={(e) => setReply(e.target.value)}
            />
          </label>
          {handle.isError && (
            <Notice tone="error" title="Couldn’t save">
              {describe(handle.error)}
            </Notice>
          )}
          <div>
            <button
              className="btn"
              disabled={handle.isPending}
              onClick={() => handle.mutate({ id: r.id, reply })}
            >
              {handle.isPending ? "Saving…" : "Mark handled"}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

function CodeLookup({ officeId }: { officeId: number | undefined }) {
  const [q, setQ] = useState("");
  const codes = useParentCodes(q, officeId);
  return (
    <div className="card stack" style={{ gap: 12 }}>
      <div>
        <h2 style={{ margin: 0 }}>Find a parent code</h2>
        <p className="small muted" style={{ margin: "4px 0 0" }}>
          For a parent who asks the office. Give the code only to the student’s
          own parent: it shows where the child is and when.
        </p>
      </div>
      <input
        className="input"
        placeholder="Student’s name"
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      {q.trim().length >= 2 &&
        (codes.isPending ? (
          <Loading />
        ) : codes.isError ? (
          <ErrorNotice error={codes.error} title="Couldn’t search" />
        ) : codes.data.students.length === 0 ? (
          <Empty>No current student of your branches by that name.</Empty>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Student</th>
                  <th>Classes</th>
                  <th>Parent code</th>
                </tr>
              </thead>
              <tbody>
                {codes.data.students.map((s) => (
                  <tr key={s.client_id}>
                    <td>
                      {s.last_name} {s.first_name}
                    </td>
                    <td className="small muted">{s.groups || "—"}</td>
                    <td>
                      <code style={{ fontSize: 16, letterSpacing: 2 }}>
                        {s.code}
                      </code>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ))}
    </div>
  );
}
