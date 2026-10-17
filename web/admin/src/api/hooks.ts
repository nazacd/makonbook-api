import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { api, call, CSRF, type Schemas } from './client';

export type Profile = Schemas['AdminProfile'];
export type Lesson = Schemas['AdminLesson'];
export type Session = Schemas['AdminAttendanceSession'];
export type Mark = Session['marks'][number];
export type MarkState = Mark['state'];
export type WriteStatus = Schemas['WriteStatus'];
export type Access = Schemas['AdminAccess'];
export type PracticeTest = Schemas['PracticeTestSummary'];
export type QuestionSummary = Schemas['PracticeQuestionSummary'];
export type Question = Schemas['PracticeQuestion'];
export type QuestionEdit = Schemas['PracticeQuestionEdit'];
export type Figure = Schemas['PracticeFigure'];

export const meKey = ['me'] as const;

/** The signed-in admin; null when signed out. */
export function useMe() {
  return useQuery({
    queryKey: meKey,
    queryFn: async (): Promise<Profile | null> => {
      const res = await api.GET('/v1/admin/me');
      if (res.response.status === 401) return null;
      return call(Promise.resolve(res));
    },
    staleTime: 5 * 60_000,
  });
}

export function useSignIn() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { login: string; password: string }) =>
      call(api.POST('/v1/admin/login', { params: { header: CSRF }, body })),
    onSuccess: (profile) => {
      qc.clear();
      qc.setQueryData(meKey, profile);
    },
  });
}

export function useSignOut() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => call(api.POST('/v1/admin/logout', { params: { header: CSRF } })),
    onSettled: () => {
      qc.clear();
      qc.setQueryData(meKey, null);
    },
  });
}

export function useLessons(date: string, officeId: number | undefined) {
  return useQuery({
    queryKey: ['lessons', date, officeId],
    queryFn: () => call(api.GET('/v1/admin/lessons', { params: { query: { date, office_id: officeId } } })),
    placeholderData: keepPreviousData,
    refetchInterval: 30_000, // attendance fills in during lessons
  });
}

export function useSession(id: string) {
  return useQuery({
    queryKey: ['session', id],
    queryFn: () => call(api.GET('/v1/admin/attendance/sessions/{id}', { params: { path: { id } } })),
    // While open, students are scanning.
    refetchInterval: (q) => (q.state.data?.status === 'open' ? 5_000 : false),
  });
}

export function useSetMark(sessionId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ clientId, state }: { clientId: number; state: 'present' | 'late' | 'absent' }) =>
      call(
        api.PUT('/v1/admin/attendance/sessions/{id}/marks/{client_id}', {
          params: { header: CSRF, path: { id: sessionId, client_id: clientId } },
          body: { state },
        }),
      ),
    onSuccess: (session) => {
      qc.setQueryData(['session', sessionId], session);
      void qc.invalidateQueries({ queryKey: ['lessons'] });
      void qc.invalidateQueries({ queryKey: ['corrections'] });
      void qc.invalidateQueries({ queryKey: ['writes'] });
    },
  });
}

export function useCorrections(params: { from?: string; to?: string; teacherId?: number; officeId?: number }) {
  return useQuery({
    queryKey: ['corrections', params],
    queryFn: () =>
      call(
        api.GET('/v1/admin/reports/corrections', {
          params: { query: { from: params.from, to: params.to, teacher_id: params.teacherId, office_id: params.officeId } },
        }),
      ),
    placeholderData: keepPreviousData,
  });
}

export function useWrites(statuses: WriteStatus[], officeId: number | undefined) {
  return useQuery({
    queryKey: ['writes', statuses, officeId],
    queryFn: () =>
      call(api.GET('/v1/admin/hollihop/writes', { params: { query: { status: statuses.join(','), office_id: officeId } } })),
    placeholderData: keepPreviousData,
  });
}

export function useRetryWrite() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => call(api.POST('/v1/admin/hollihop/writes/{id}/retry', { params: { header: CSRF, path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['writes'] }),
  });
}

export function useDataQuality(officeId: number | undefined) {
  return useQuery({
    queryKey: ['data-quality', officeId],
    queryFn: () => call(api.GET('/v1/admin/data-quality', { params: { query: { office_id: officeId } } })),
    placeholderData: keepPreviousData,
  });
}

export function useAccess(enabled: boolean) {
  return useQuery({
    queryKey: ['access'],
    queryFn: () => call(api.GET('/v1/admin/access')),
    enabled, // super admins only
  });
}

type Member = { type: 'employee' | 'teacher'; id: number };

export function useGrant() {
  const qc = useQueryClient();
  return useMutation({
    // The panel only makes branch admins and editors; super admins are managed on the server.
    mutationFn: ({ member, officeIds, editor }: { member: Member; officeIds: number[]; editor: boolean }) =>
      call(
        api.PUT('/v1/admin/access/{member_type}/{member_id}', {
          params: { header: CSRF, path: { member_type: member.type, member_id: member.id } },
          body: { office_ids: officeIds, editor },
        }),
      ),
    onSuccess: (list) => qc.setQueryData(['access'], list),
  });
}

export function useRevoke() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (member: Member) =>
      call(
        api.DELETE('/v1/admin/access/{member_type}/{member_id}', {
          params: { header: CSRF, path: { member_type: member.type, member_id: member.id } },
        }),
      ),
    onSuccess: (list) => qc.setQueryData(['access'], list),
  });
}

export function useStaffSearch(q: string) {
  return useQuery({
    queryKey: ['staff', q],
    queryFn: () => call(api.GET('/v1/admin/staff', { params: { query: { q } } })),
    enabled: q.trim().length >= 2,
    staleTime: 60_000,
    retry: false, // a person is waiting on the answer
  });
}

// ---- Practice tests (editors) ----

export function usePracticeTests() {
  return useQuery({
    queryKey: ['practice', 'tests'],
    queryFn: () => call(api.GET('/v1/admin/practice/tests')),
  });
}

export function usePracticeTest(code: string) {
  return useQuery({
    queryKey: ['practice', 'test', code],
    queryFn: () => call(api.GET('/v1/admin/practice/tests/{code}', { params: { path: { code } } })),
  });
}

export type QueueFilter = {
  risk?: 'red' | 'yellow' | 'green';
  status?: 'draft' | 'review' | 'approved';
  subject?: 'math' | 'english';
  offset: number;
};

export function usePracticeQueue(f: QueueFilter) {
  return useQuery({
    queryKey: ['practice', 'queue', f],
    queryFn: () => call(api.GET('/v1/admin/practice/questions', { params: { query: { ...f, limit: 50 } } })),
    placeholderData: keepPreviousData,
  });
}

export function usePracticeQuestion(id: string) {
  return useQuery({
    queryKey: ['practice', 'question', id],
    queryFn: () => call(api.GET('/v1/admin/practice/questions/{id}', { params: { path: { id } } })),
    staleTime: Infinity, // an open editor must not be refreshed under the person's hands
    refetchOnWindowFocus: false,
  });
}

export function useSaveQuestion(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: QuestionEdit) =>
      call(api.PUT('/v1/admin/practice/questions/{id}', { params: { header: CSRF, path: { id } }, body })),
    onSuccess: (q) => {
      qc.setQueryData(['practice', 'question', id], q);
      void qc.invalidateQueries({ queryKey: ['practice', 'tests'] });
      void qc.invalidateQueries({ queryKey: ['practice', 'test'] });
      void qc.invalidateQueries({ queryKey: ['practice', 'queue'] });
    },
  });
}

export function useTaxonomy() {
  return useQuery({
    queryKey: ['practice', 'taxonomy'],
    queryFn: () => call(api.GET('/v1/admin/practice/taxonomy')),
    staleTime: Infinity,
  });
}
