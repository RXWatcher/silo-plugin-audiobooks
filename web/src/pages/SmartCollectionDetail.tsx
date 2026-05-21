import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { ArrowLeft, Plus, Save, Trash2 } from 'lucide-react';
import { api } from '@/api/client';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { Skeleton } from '@/components/ui/skeleton';
import { Switch } from '@/components/ui/switch';
import type {
  AudiobookSummary,
  SmartCollectionGroup,
  SmartCollectionQuery,
  SmartCollectionRule,
  SmartCollectionSort,
} from '@/api/types';

// Smart Collection detail / builder. Two columns when wide: rules
// editor on the left, live preview on the right. Stacks on
// narrow screens.
//
// The field catalog + per-field op set is mirrored from
// internal/smartcoll/query.go. When the backend's catalog grows,
// add the field here; the SPA validates per-field op compatibility
// client-side as a UX nicety but the server validates again on
// save.

const FIELDS: { value: string; label: string; ops: string[]; personalized?: boolean }[] = [
  { value: 'title', label: 'Title', ops: ['is', 'is_not', 'contains'] },
  { value: 'author', label: 'Author', ops: ['is', 'is_not', 'contains'] },
  { value: 'narrator', label: 'Narrator', ops: ['is', 'is_not', 'contains'] },
  { value: 'series', label: 'Series', ops: ['is', 'is_not', 'contains'] },
  { value: 'genre', label: 'Genre', ops: ['is', 'is_not', 'contains'] },
  { value: 'year', label: 'Year', ops: ['is', 'is_not', 'gt', 'gte', 'lt', 'lte', 'between'] },
  { value: 'rating', label: 'Rating', ops: ['gt', 'gte', 'lt', 'lte', 'between'] },
  { value: 'language', label: 'Language', ops: ['is', 'is_not'] },
  { value: 'publisher', label: 'Publisher', ops: ['is', 'is_not', 'contains'] },
  { value: 'duration_seconds', label: 'Duration (seconds)', ops: ['gt', 'gte', 'lt', 'lte', 'between'] },
  { value: 'added_at', label: 'Added', ops: ['gt', 'lt', 'between', 'in_last'] },
  { value: 'finished', label: 'Finished', ops: ['is'], personalized: true },
  { value: 'in_progress', label: 'In progress', ops: ['is'], personalized: true },
  { value: 'last_played', label: 'Last played', ops: ['gt', 'gte', 'lt', 'lte', 'between', 'in_last'], personalized: true },
  { value: 'abandoned', label: 'Abandoned', ops: ['is'], personalized: true },
  { value: 'bookmark_count', label: 'Bookmark count', ops: ['gt', 'gte', 'lt', 'lte', 'between'], personalized: true },
];

const OP_LABELS: Record<string, string> = {
  is: 'is',
  is_not: 'is not',
  contains: 'contains',
  gt: '>',
  gte: '≥',
  lt: '<',
  lte: '≤',
  between: 'between',
  in_last: 'in the last',
};

const SORT_FIELDS: { value: string; label: string }[] = [
  { value: 'added_at', label: 'Added (newest first)' },
  { value: 'title', label: 'Title' },
  { value: 'year', label: 'Year' },
  { value: 'rating', label: 'Rating' },
  { value: 'duration_seconds', label: 'Duration' },
  { value: 'random', label: 'Random' },
  { value: 'progress', label: 'Progress' },
  { value: 'last_played', label: 'Last played' },
  { value: 'plays', label: 'Play count' },
];

export default function SmartCollectionDetail() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const qc = useQueryClient();

  const collection = useQuery({
    queryKey: ['smart-collection', id],
    queryFn: () => api.getSmartCollection(id),
    enabled: !!id,
  });

  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [isPinned, setIsPinned] = useState(false);
  const [isPublic, setIsPublic] = useState(false);
  const [query, setQuery] = useState<SmartCollectionQuery>({
    match: 'all',
    groups: [],
    sort: { field: 'added_at', order: 'desc' },
  });

  // Hydrate form state from the fetched collection. Reset whenever
  // the source row's updatedAt advances — covers the case where
  // another tab edited the collection.
  useEffect(() => {
    if (!collection.data) return;
    setName(collection.data.name);
    setDescription(collection.data.description ?? '');
    setIsPinned(collection.data.isPinned);
    setIsPublic(collection.data.isPublic);
    setQuery(
      collection.data.queryDef ?? {
        match: 'all',
        groups: [],
        sort: { field: 'added_at', order: 'desc' },
      },
    );
  }, [collection.data?.updatedAt, collection.data?.id]);

  const saveMutation = useMutation({
    mutationFn: () =>
      api.updateSmartCollection(id, {
        name,
        description,
        is_pinned: isPinned,
        is_public: isPublic,
        query_def: query,
      }),
    onSuccess: () => {
      toast.success('Saved');
      qc.invalidateQueries({ queryKey: ['smart-collection', id] });
      qc.invalidateQueries({ queryKey: ['smart-collection-items', id] });
      qc.invalidateQueries({ queryKey: ['smart-collections'] });
    },
    onError: (err) => toast.error(`Save failed: ${err}`),
  });

  const preview = useQuery({
    queryKey: ['smart-collection-items', id],
    queryFn: () => api.getSmartCollectionItems(id, 0, 24),
    enabled: !!id && !!collection.data,
  });

  if (collection.isLoading) return <SkeletonView />;
  if (collection.isError) {
    return (
      <Card className="bg-surface p-6 text-sm">
        Smart collection not found.{' '}
        <button className="underline" onClick={() => navigate('/smart-collections')}>
          Back
        </button>
      </Card>
    );
  }

  const updateGroup = (i: number, group: SmartCollectionGroup) => {
    setQuery((q) => ({ ...q, groups: q.groups.map((g, idx) => (idx === i ? group : g)) }));
  };
  const removeGroup = (i: number) => {
    setQuery((q) => ({ ...q, groups: q.groups.filter((_, idx) => idx !== i) }));
  };
  const addGroup = () => {
    setQuery((q) => ({
      ...q,
      groups: [...q.groups, { match: 'all', rules: [{ field: 'title', op: 'contains', value: '' }] }],
    }));
  };

  return (
    <div className="space-y-6">
      <header className="flex items-center justify-between">
        <button
          type="button"
          onClick={() => navigate('/smart-collections')}
          className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-sm"
        >
          <ArrowLeft className="size-4" />
          All smart collections
        </button>
        <Button onClick={() => saveMutation.mutate()} disabled={saveMutation.isPending}>
          <Save className="size-4" />
          <span className="ml-1">Save</span>
        </Button>
      </header>

      <div className="grid gap-6 lg:grid-cols-[1fr,1fr]">
        <div className="space-y-4">
          <Card className="bg-surface space-y-3 p-4">
            <div>
              <Label htmlFor="name">Name</Label>
              <Input id="name" value={name} onChange={(e) => setName(e.target.value)} />
            </div>
            <div>
              <Label htmlFor="description">Description</Label>
              <Input
                id="description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </div>
            <div className="flex gap-6">
              <div className="flex items-center gap-2">
                <Switch checked={isPinned} onCheckedChange={setIsPinned} />
                <Label>Pinned</Label>
              </div>
              <div className="flex items-center gap-2">
                <Switch checked={isPublic} onCheckedChange={setIsPublic} />
                <Label>Public</Label>
              </div>
            </div>
          </Card>

          <Card className="bg-surface space-y-3 p-4">
            <div className="flex items-center justify-between">
              <h3 className="font-medium">Rules</h3>
              <Select
                value={query.match}
                onValueChange={(v: 'all' | 'any') => setQuery((q) => ({ ...q, match: v }))}
              >
                <SelectTrigger className="w-32">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">Match all</SelectItem>
                  <SelectItem value="any">Match any</SelectItem>
                </SelectContent>
              </Select>
            </div>
            {query.groups.length === 0 ? (
              <p className="text-muted-foreground text-xs">
                No rules yet — this collection currently matches every audiobook in your
                libraries.
              </p>
            ) : (
              query.groups.map((g, i) => (
                <GroupEditor
                  key={i}
                  group={g}
                  onChange={(g2) => updateGroup(i, g2)}
                  onRemove={() => removeGroup(i)}
                />
              ))
            )}
            <Button variant="secondary" onClick={addGroup}>
              <Plus className="size-4" />
              <span className="ml-1">Add rule group</span>
            </Button>
          </Card>

          <Card className="bg-surface space-y-3 p-4">
            <h3 className="font-medium">Sort + limit</h3>
            <SortEditor
              sort={query.sort}
              onChange={(s) => setQuery((q) => ({ ...q, sort: s }))}
            />
            <div>
              <Label>Limit</Label>
              <Input
                type="number"
                min={0}
                placeholder="unlimited"
                value={query.limit ?? ''}
                onChange={(e) => {
                  const n = parseInt(e.target.value, 10);
                  setQuery((q) => ({
                    ...q,
                    limit: Number.isFinite(n) && n > 0 ? n : undefined,
                  }));
                }}
              />
            </div>
          </Card>
        </div>

        <div className="space-y-3">
          <Card className="bg-surface p-4">
            <div className="mb-3 flex items-center justify-between">
              <h3 className="font-medium">Preview</h3>
              <span className="text-muted-foreground text-xs">
                {preview.data?.total ?? 0} match{preview.data?.total === 1 ? '' : 'es'}
              </span>
            </div>
            {preview.isLoading ? (
              <Skeleton className="h-32 w-full" />
            ) : (preview.data?.items ?? []).length === 0 ? (
              <p className="text-muted-foreground text-sm">No matches yet.</p>
            ) : (
              <div className="space-y-2">
                {preview.data!.items.slice(0, 12).map((book: AudiobookSummary) => (
                  <div
                    key={book.id}
                    className="hover:bg-surface-hover flex items-center gap-3 rounded-md p-2 text-sm"
                  >
                    <div className="bg-muted size-10 shrink-0 overflow-hidden rounded">
                      {book.cover_url && (
                        <img
                          src={book.cover_url}
                          alt=""
                          loading="lazy"
                          className="size-full object-cover"
                        />
                      )}
                    </div>
                    <div className="min-w-0 flex-1">
                      <div className="truncate font-medium">{book.title}</div>
                      <div className="text-muted-foreground truncate text-xs">
                        {(book.authors ?? []).join(', ')}
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </Card>
        </div>
      </div>
    </div>
  );
}

// GroupEditor renders one group's match + rules. Rules belong to a
// single group; the top-level "match all/any" combines groups.
function GroupEditor({
  group,
  onChange,
  onRemove,
}: {
  group: SmartCollectionGroup;
  onChange: (g: SmartCollectionGroup) => void;
  onRemove: () => void;
}) {
  const updateRule = (i: number, r: SmartCollectionRule) => {
    onChange({ ...group, rules: group.rules.map((x, idx) => (idx === i ? r : x)) });
  };
  const removeRule = (i: number) => {
    onChange({ ...group, rules: group.rules.filter((_, idx) => idx !== i) });
  };
  const addRule = () => {
    onChange({
      ...group,
      rules: [...group.rules, { field: 'title', op: 'contains', value: '' }],
    });
  };
  return (
    <Card className="bg-background space-y-2 border-dashed p-3">
      <div className="flex items-center justify-between">
        <Select
          value={group.match}
          onValueChange={(v: 'all' | 'any') => onChange({ ...group, match: v })}
        >
          <SelectTrigger className="w-32">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All rules</SelectItem>
            <SelectItem value="any">Any rule</SelectItem>
          </SelectContent>
        </Select>
        <Button size="icon" variant="ghost" onClick={onRemove} title="Remove group">
          <Trash2 className="size-4" />
        </Button>
      </div>
      {group.rules.map((r, i) => (
        <RuleEditor
          key={i}
          rule={r}
          onChange={(r2) => updateRule(i, r2)}
          onRemove={() => removeRule(i)}
        />
      ))}
      <Button variant="ghost" size="sm" onClick={addRule}>
        <Plus className="size-4" />
        <span className="ml-1">Add rule</span>
      </Button>
    </Card>
  );
}

function RuleEditor({
  rule,
  onChange,
  onRemove,
}: {
  rule: SmartCollectionRule;
  onChange: (r: SmartCollectionRule) => void;
  onRemove: () => void;
}) {
  const field = FIELDS.find((f) => f.value === rule.field) ?? FIELDS[0];
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Select
        value={rule.field}
        onValueChange={(v) => {
          const nf = FIELDS.find((f) => f.value === v) ?? FIELDS[0];
          // Reset op when the field changes to keep ops field-valid.
          onChange({ ...rule, field: v, op: nf.ops[0], value: '' });
        }}
      >
        <SelectTrigger className="w-36">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {FIELDS.map((f) => (
            <SelectItem key={f.value} value={f.value}>
              {f.label}
              {f.personalized ? ' *' : ''}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select value={rule.op} onValueChange={(v) => onChange({ ...rule, op: v })}>
        <SelectTrigger className="w-28">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {field.ops.map((op) => (
            <SelectItem key={op} value={op}>
              {OP_LABELS[op] ?? op}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Input
        value={String(rule.value ?? '')}
        onChange={(e) => {
          // Coerce to a number when the field is numeric; the server
          // accepts string-encoded numbers but typed values keep the
          // wire shape clean.
          const v = e.target.value;
          if (['year', 'rating', 'duration_seconds', 'bookmark_count'].includes(rule.field)) {
            const n = parseFloat(v);
            onChange({ ...rule, value: Number.isFinite(n) ? n : v });
          } else {
            onChange({ ...rule, value: v });
          }
        }}
        className="flex-1 min-w-[8rem]"
        placeholder={field.value === 'added_at' ? 'e.g. 30 days' : 'value'}
      />
      <Button size="icon" variant="ghost" onClick={onRemove} title="Remove rule">
        <Trash2 className="size-4" />
      </Button>
    </div>
  );
}

function SortEditor({
  sort,
  onChange,
}: {
  sort: SmartCollectionSort;
  onChange: (s: SmartCollectionSort) => void;
}) {
  return (
    <div className="flex gap-2">
      <Select value={sort.field} onValueChange={(v) => onChange({ ...sort, field: v })}>
        <SelectTrigger className="flex-1">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {SORT_FIELDS.map((s) => (
            <SelectItem key={s.value} value={s.value}>
              {s.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select
        value={sort.order}
        onValueChange={(v: 'asc' | 'desc') => onChange({ ...sort, order: v })}
      >
        <SelectTrigger className="w-28">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="desc">Desc</SelectItem>
          <SelectItem value="asc">Asc</SelectItem>
        </SelectContent>
      </Select>
    </div>
  );
}

function SkeletonView() {
  return (
    <div className="space-y-4">
      <Skeleton className="h-9 w-32" />
      <Skeleton className="h-64 w-full" />
    </div>
  );
}
