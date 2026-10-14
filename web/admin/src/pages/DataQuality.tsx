import { useDataQuality } from '../api/hooks';
import { Empty, ErrorNotice, Loading, OfficeSelect, useOfficeParam } from '../components/ui';
import { shortDate } from '../lib/dates';

const KIND: Record<string, string> = {
  phone_missing: 'No phone number',
  phone_invalid: 'Phone number isn’t valid',
  phone_duplicate: 'Phone number shared',
};

// People who can't sign in to the app until their phone is fixed in HolliHop.
export function DataQualityPage() {
  const [officeId, setOfficeId] = useOfficeParam();
  const issues = useDataQuality(officeId);
  const list = issues.data?.issues ?? [];

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>Data quality</h1>
          <p className="muted">
            These people can’t sign in to the app. Fix the phone number in HolliHop; the next sync (within 15 minutes) clears
            the problem.
          </p>
        </div>
        <div className="toolbar">
          <OfficeSelect value={officeId} onChange={setOfficeId} />
        </div>
      </div>

      {issues.isPending ? (
        <Loading />
      ) : issues.isError ? (
        <ErrorNotice error={issues.error} title="Couldn’t load the problems" />
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Person</th>
                <th>Problem</th>
                <th>Phone in HolliHop</th>
                <th>Details</th>
                <th>Since</th>
              </tr>
            </thead>
            <tbody>
              {list.map((i) => (
                <tr key={i.id}>
                  <td>
                    {i.last_name} {i.first_name}
                    <div className="small faint">
                      {i.person_kind === 'student' ? 'Student' : 'Teacher'} · HolliHop id {i.person_id}
                    </div>
                  </td>
                  <td>
                    <span className="chip warning">{KIND[i.kind] ?? i.kind}</span>
                  </td>
                  <td className="tabular">{i.phone || <span className="faint">—</span>}</td>
                  <td className="small muted">{i.detail}</td>
                  <td className="small muted nowrap">{shortDate(i.since.slice(0, 10))}</td>
                </tr>
              ))}
              {list.length === 0 && (
                <tr>
                  <td colSpan={5}>
                    <Empty>Everyone’s phone number is fine.</Empty>
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
