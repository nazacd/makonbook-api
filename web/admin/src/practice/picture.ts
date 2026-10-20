import { ApiError } from '../api/client';
import type { QuestionEdit } from '../api/hooks';

/** Draws the question as the form has it (Export image) and downloads the
 * PNG: the same template the Telegram group's pictures use. */
export async function downloadPicture(id: string, test: string | undefined, body: QuestionEdit): Promise<void> {
  const q = test ? `?test=${encodeURIComponent(test)}` : '';
  let res: Response;
  try {
    res = await fetch(`/v1/admin/practice/questions/${id}/picture${q}`, {
      method: 'POST',
      body: JSON.stringify(body),
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', 'X-MakonBook-Admin': '1' },
    });
  } catch {
    throw new ApiError(0, 'network', 'Can’t reach MakonBook. Check the connection and try again.');
  }
  if (!res.ok) {
    const json = (await res.json().catch(() => ({}))) as { error?: { code?: string; message?: string } };
    throw new ApiError(res.status, json.error?.code ?? 'unknown', json.error?.message ?? 'Couldn’t draw the picture.');
  }
  const name = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? 'question.png';
  const url = URL.createObjectURL(await res.blob());
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  a.click();
  window.setTimeout(() => URL.revokeObjectURL(url), 10_000);
}
