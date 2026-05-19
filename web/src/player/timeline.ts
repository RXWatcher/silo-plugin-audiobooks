import type { AudiobookChapter, AudiobookFile } from '@/api/types';

export type FileTimelineEntry = {
  fileIndex: number;
  fileOrdinal: number;
  start: number;
  end: number;
  duration: number;
};

export type FileTimePosition = {
  fileIndex: number;
  fileOrdinal: number;
  fileTime: number;
  fileStart: number;
};

export function buildFileTimeline(files: AudiobookFile[]): FileTimelineEntry[] {
  let start = 0;
  return files.map((file, fileOrdinal) => {
    const duration = Math.max(0, file.duration_seconds || 0);
    const entry = {
      fileIndex: file.index,
      fileOrdinal,
      start,
      end: start + duration,
      duration,
    };
    start += duration;
    return entry;
  });
}

export function timelineDuration(timeline: FileTimelineEntry[]): number {
  return timeline.at(-1)?.end ?? 0;
}

export function positionToFileTime(
  timeline: FileTimelineEntry[],
  bookTime: number,
): FileTimePosition {
  if (timeline.length === 0) {
    return { fileIndex: 0, fileOrdinal: 0, fileTime: 0, fileStart: 0 };
  }
  const duration = timelineDuration(timeline);
  const clamped = Math.max(0, Math.min(bookTime, duration));
  const entry =
    timeline.find((item) => clamped >= item.start && clamped < item.end) ??
    timeline[timeline.length - 1];
  return {
    fileIndex: entry.fileIndex,
    fileOrdinal: entry.fileOrdinal,
    fileTime: Math.max(0, Math.min(clamped - entry.start, entry.duration)),
    fileStart: entry.start,
  };
}

export function fileTimeToBookTime(
  timeline: FileTimelineEntry[],
  fileOrdinal: number,
  fileTime: number,
): number {
  const entry = timeline[fileOrdinal] ?? timeline[0];
  if (!entry) return 0;
  return entry.start + Math.max(0, Math.min(fileTime, entry.duration));
}

export function chapterAt(
  chapters: AudiobookChapter[] | undefined,
  bookTime: number,
): AudiobookChapter | undefined {
  if (!chapters?.length) return undefined;
  return (
    chapters.find(
      (chapter) => bookTime >= chapter.start_seconds && bookTime < chapter.end_seconds,
    ) ?? chapters[chapters.length - 1]
  );
}
