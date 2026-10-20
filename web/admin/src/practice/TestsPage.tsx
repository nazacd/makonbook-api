import { Link, useNavigate } from "react-router";

import { usePracticeTests } from "../api/hooks";
import { Empty, ErrorNotice, Loading, Stat } from "../components/ui";

// Every loaded test and how far its review has come. A test opens to
// students when every question in it is approved.
export function PracticeTestsPage() {
  const tests = usePracticeTests();
  const navigate = useNavigate();
  const list = tests.data?.tests ?? [];
  const sum = (k: "questions" | "approved" | "red" | "yellow") =>
    list.reduce((n, t) => n + t[k], 0);

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>Practice tests</h1>
          <p className="muted">
            Reviewed by the AI, loaded from the question bank. A test opens to
            students once every question in it is approved.
          </p>
        </div>
        <Link className="btn" to="/practice/queue">
          Questions to fix
        </Link>
      </div>

      {tests.isPending ? (
        <Loading />
      ) : tests.isError ? (
        <ErrorNotice error={tests.error} />
      ) : list.length === 0 ? (
        <div className="table-wrap">
          <Empty>
            No tests loaded yet. On the server: makonbook practice import (see
            DEPLOY.md).
          </Empty>
        </div>
      ) : (
        <>
          <div className="stats">
            <Stat value={list.length} label="Tests" />
            <Stat
              value={list.filter((t) => t.open).length}
              label="Open to students"
            />
            <Stat value={sum("red")} label="Red questions to decide" />
            <Stat value={sum("yellow")} label="Yellow questions to check" />
            <Stat
              value={sum("approved")}
              label={`Approved of ${sum("questions")}`}
            />
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Test</th>
                  <th>Approved</th>
                  <th className="num">Red</th>
                  <th className="num">Yellow</th>
                  <th className="num">Shared</th>
                  <th>Students</th>
                </tr>
              </thead>
              <tbody>
                {list.map((t) => {
                  const open = () => navigate(`/practice/tests/${t.code}`);
                  return (
                    <tr
                      key={t.code}
                      className="clickable"
                      tabIndex={0}
                      onClick={open}
                      onKeyDown={(e) =>
                        (e.key === "Enter" || e.key === " ") && open()
                      }
                      aria-label={`${t.code}: open`}
                    >
                      <td>
                        <b>{t.title}</b>
                      </td>
                      <td>
                        <div className="progress" aria-hidden>
                          <span
                            style={{
                              width: `${(100 * t.approved) / Math.max(1, t.questions)}%`,
                            }}
                          />
                        </div>
                        <span className="small muted tabular">
                          {t.approved}/{t.questions}
                        </span>
                      </td>
                      <td className="num">
                        {t.red || <span className="faint">0</span>}
                      </td>
                      <td className="num">
                        {t.yellow || <span className="faint">0</span>}
                      </td>
                      <td
                        className="num"
                        title="Questions other tests use too: fixing one fixes it everywhere"
                      >
                        {t.shared || <span className="faint">0</span>}
                      </td>
                      <td>
                        {t.published > 0 ? (
                          <span
                            className={`chip ${t.open ? "success" : "plain"}`}
                            title={
                              t.open
                                ? "Students have this version"
                                : "Students keep this version until the test is approved again"
                            }
                          >
                            Open · v{t.published}
                          </span>
                        ) : t.open ? (
                          <span
                            className="chip plain"
                            title="Published within a few minutes"
                          >
                            Publishing
                          </span>
                        ) : (
                          <span className="chip plain">Locked</span>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  );
}
