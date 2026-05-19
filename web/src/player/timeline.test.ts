import { describe, expect, test } from 'vitest';
import {
  buildFileTimeline,
  chapterAt,
  fileTimeToBookTime,
  positionToFileTime,
} from './timeline';

const files = [
  { index: 7, mime_type: 'audio/mpeg', format: 'mp3', duration_seconds: 100 },
  { index: 9, mime_type: 'audio/mpeg', format: 'mp3', duration_seconds: 50 },
  { index: 10, mime_type: 'audio/mpeg', format: 'mp3', duration_seconds: 75 },
];

describe('audiobook whole-book timeline', () => {
  test('maps whole-book seconds to the active file and local file time', () => {
    const timeline = buildFileTimeline(files);

    expect(positionToFileTime(timeline, 0)).toEqual({
      fileIndex: 7,
      fileOrdinal: 0,
      fileTime: 0,
      fileStart: 0,
    });
    expect(positionToFileTime(timeline, 125)).toEqual({
      fileIndex: 9,
      fileOrdinal: 1,
      fileTime: 25,
      fileStart: 100,
    });
    expect(positionToFileTime(timeline, 225)).toEqual({
      fileIndex: 10,
      fileOrdinal: 2,
      fileTime: 75,
      fileStart: 150,
    });
  });

  test('maps file-local time back to whole-book seconds', () => {
    const timeline = buildFileTimeline(files);

    expect(fileTimeToBookTime(timeline, 0, 12)).toBe(12);
    expect(fileTimeToBookTime(timeline, 1, 12)).toBe(112);
    expect(fileTimeToBookTime(timeline, 2, 100)).toBe(225);
  });

  test('derives the active chapter from whole-book seconds', () => {
    const chapters = [
      { title: 'Opening', start_seconds: 0, end_seconds: 45 },
      { title: 'Middle', start_seconds: 45, end_seconds: 120 },
      { title: 'Ending', start_seconds: 120, end_seconds: 225 },
    ];

    expect(chapterAt(chapters, 44)?.title).toBe('Opening');
    expect(chapterAt(chapters, 45)?.title).toBe('Middle');
    expect(chapterAt(chapters, 224)?.title).toBe('Ending');
    expect(chapterAt(chapters, 225)?.title).toBe('Ending');
  });
});
