import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Lock, Plus, Trash2 } from 'lucide-react';
import { api } from '@/api/client';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';
import { Switch } from '@/components/ui/switch';
import type { ContentRestriction } from '@/api/types';

// Per-user content restrictions admin. Lists every user that has
// restrictions configured + lets admin pick a user + edit their
// rules (blocked genres / tags / authors / narrators + explicit
// flag + library scope).

export default function ContentRestrictionsTab() {
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: ['admin-content-restrictions'],
    queryFn: () => api.listContentRestrictions(),
  });
  const [editing, setEditing] = useState<ContentRestriction | null>(null);
  const [userIdDraft, setUserIdDraft] = useState('');

  const remove = useMutation({
    mutationFn: (userId: string) => api.deleteContentRestriction(userId),
    onSuccess: () => {
      toast.success('Restriction removed');
      qc.invalidateQueries({ queryKey: ['admin-content-restrictions'] });
    },
  });

  return (
    <div className="space-y-4">
      <Card className="bg-surface p-4">
        <div className="mb-3 flex items-center gap-2">
          <Lock className="size-5" />
          <h3 className="font-medium">Content restrictions</h3>
        </div>
        <p className="text-muted-foreground mb-3 text-xs">
          Set per-user filters for genres / tags / authors / narrators.
          Restrictions apply at catalog read time; explicit blocks hide flagged
          items from browse + search.
        </p>

        {list.isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : (list.data?.items ?? []).length === 0 ? (
          <p className="text-muted-foreground text-sm">No restrictions yet.</p>
        ) : (
          <ul className="space-y-2">
            {list.data!.items.map((r) => (
              <li
                key={r.user_id}
                className="bg-background flex items-center justify-between rounded-md border border-dashed p-3 text-sm"
              >
                <div className="min-w-0 flex-1">
                  <div className="font-medium">{r.user_id}</div>
                  <div className="text-muted-foreground text-xs">
                    {summarise(r)}
                  </div>
                </div>
                <div className="flex gap-1">
                  <Button size="sm" variant="ghost" onClick={() => setEditing(r)}>
                    Edit
                  </Button>
                  <Button
                    size="icon"
                    variant="ghost"
                    onClick={() => remove.mutate(r.user_id)}
                    title="Remove"
                  >
                    <Trash2 className="size-4" />
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}

        <div className="bg-border my-4 h-px" />

        <div className="flex items-end gap-2">
          <div className="flex-1">
            <Label htmlFor="user-id">Add user (by id)</Label>
            <Input
              id="user-id"
              value={userIdDraft}
              onChange={(e) => setUserIdDraft(e.target.value)}
              placeholder="userId"
            />
          </div>
          <Button
            onClick={() => {
              if (!userIdDraft.trim()) return;
              setEditing({
                user_id: userIdDraft.trim(),
                blocked_genres: [],
                blocked_tags: [],
                blocked_authors: [],
                blocked_narrators: [],
                block_explicit: false,
              });
              setUserIdDraft('');
            }}
          >
            <Plus className="size-4" />
            <span className="ml-1">Configure</span>
          </Button>
        </div>
      </Card>

      {editing && (
        <RestrictionEditor
          restriction={editing}
          onCancel={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            qc.invalidateQueries({ queryKey: ['admin-content-restrictions'] });
          }}
        />
      )}
    </div>
  );
}

function summarise(r: ContentRestriction): string {
  const parts: string[] = [];
  if (r.blocked_genres?.length) parts.push(`${r.blocked_genres.length} genres`);
  if (r.blocked_tags?.length) parts.push(`${r.blocked_tags.length} tags`);
  if (r.blocked_authors?.length)
    parts.push(`${r.blocked_authors.length} authors`);
  if (r.blocked_narrators?.length)
    parts.push(`${r.blocked_narrators.length} narrators`);
  if (r.block_explicit) parts.push('explicit blocked');
  if (r.library_ids?.length) parts.push(`${r.library_ids.length} libraries`);
  return parts.join(' · ') || 'no rules set';
}

function RestrictionEditor({
  restriction,
  onCancel,
  onSaved,
}: {
  restriction: ContentRestriction;
  onCancel: () => void;
  onSaved: () => void;
}) {
  const [blockedGenres, setBlockedGenres] = useState(
    (restriction.blocked_genres ?? []).join(', '),
  );
  const [blockedTags, setBlockedTags] = useState(
    (restriction.blocked_tags ?? []).join(', '),
  );
  const [blockedAuthors, setBlockedAuthors] = useState(
    (restriction.blocked_authors ?? []).join(', '),
  );
  const [blockedNarrators, setBlockedNarrators] = useState(
    (restriction.blocked_narrators ?? []).join(', '),
  );
  const [blockExplicit, setBlockExplicit] = useState(
    !!restriction.block_explicit,
  );

  const save = useMutation({
    mutationFn: () =>
      api.putContentRestriction(restriction.user_id, {
        blocked_genres: splitCSV(blockedGenres),
        blocked_tags: splitCSV(blockedTags),
        blocked_authors: splitCSV(blockedAuthors),
        blocked_narrators: splitCSV(blockedNarrators),
        block_explicit: blockExplicit,
      }),
    onSuccess: () => {
      toast.success('Saved');
      onSaved();
    },
    onError: (err) => toast.error(`Save failed: ${err}`),
  });

  return (
    <Card className="bg-surface space-y-3 p-4">
      <h4 className="font-medium">Editing: {restriction.user_id}</h4>
      <FieldList
        label="Blocked genres (comma-separated)"
        value={blockedGenres}
        onChange={setBlockedGenres}
      />
      <FieldList
        label="Blocked tags"
        value={blockedTags}
        onChange={setBlockedTags}
      />
      <FieldList
        label="Blocked authors"
        value={blockedAuthors}
        onChange={setBlockedAuthors}
      />
      <FieldList
        label="Blocked narrators"
        value={blockedNarrators}
        onChange={setBlockedNarrators}
      />
      <div className="flex items-center justify-between">
        <Label>Block explicit content</Label>
        <Switch checked={blockExplicit} onCheckedChange={setBlockExplicit} />
      </div>
      <div className="flex gap-2">
        <Button onClick={() => save.mutate()} disabled={save.isPending}>
          Save
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </Card>
  );
}

function FieldList({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div>
      <Label>{label}</Label>
      <Input value={value} onChange={(e) => onChange(e.target.value)} />
    </div>
  );
}

function splitCSV(s: string): string[] {
  return s
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean);
}
