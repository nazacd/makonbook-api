import { Link, useNavigate, useParams } from "react-router";

import { usePracticeTest, type QuestionSummary } from "../api/hooks";
import { ErrorNotice, Loading } from "../components/ui";
import { AlsoIn, RiskChip, StatusChip, SUBJECT } from "./common";

// One test, module by module, worst questions marked.
export function PracticeTestPage() {
  const { code = "" } = useParams();
  const test = usePracticeTest(code);
  const navigate = useNavigate();

  if (test.isPending) return <Loading />;
  if (test.isError)
    return <ErrorNotice error={test.error} title="Couldn’t load this test" />;
  const t = test.data;

  const groups: { title: string; rows: QuestionSummary[] }[] = [];
  for (const q of t.slots) {
    const title = `${SUBJECT[q.subject] ?? q.subject} · Module ${q.position?.module}`;
    const g = groups[groups.length - 1];
    if (g && g.title === title) g.rows.push(q);
    else groups.push({ title, rows: [q] });
  }

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <div className="small">
            <Link to="/practice">Practice tests</Link>
          </div>
          <h1>{t.title}</h1>
          <p className="muted">
            {t.approved}/{t.questions} approved · {t.red} red, {t.yellow} yellow
            to go ·{" "}
            {t.published > 0
              ? `students have version ${t.published}${t.open ? "" : "; a new one is published once every question is approved again"}`
              : t.open
                ? "publishing for students within a few minutes"
                : "locked until every question is approved"}
          </p>
        </div>
      </div>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th className="num">#</th>
              <th>AI</th>
              <th>Question</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            {groups.map((g) => [
              <tr key={g.title} className="group-row">
                <td colSpan={4}>{g.title}</td>
              </tr>,
              ...g.rows.map((q) => {
                const open = () =>
                  navigate(
                    `/practice/questions/${q.id}?test=${encodeURIComponent(t.code)}`,
                  );
                return (
                  <tr
                    key={`${q.position?.subject}-${q.position?.module}-${q.position?.position}`}
                    className="clickable"
                    tabIndex={0}
                    onClick={open}
                    onKeyDown={(e) =>
                      (e.key === "Enter" || e.key === " ") && open()
                    }
                  >
                    <td className="num faint">{q.position?.position}</td>
                    <td>
                      <RiskChip risk={q.ai_risk} />
                    </td>
                    <td>
                      <div className="clamp">
                        {q.preview || <span className="faint">(empty)</span>}
                      </div>
                      <AlsoIn places={q.places} test={t.code} />
                    </td>
                    <td>
                      <StatusChip status={q.status} />
                    </td>
                  </tr>
                );
              }),
            ])}
          </tbody>
        </table>
      </div>
    </div>
  );
}
