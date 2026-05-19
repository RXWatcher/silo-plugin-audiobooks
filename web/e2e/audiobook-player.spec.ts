import { expect, test } from '@playwright/test';

const detail = {
  audiobook: {
    id: 'book1',
    title: 'Test Audiobook',
    authors: ['A. Writer'],
    narrators: ['N. Reader'],
    duration_seconds: 180,
    files: [
      { index: 0, mime_type: 'audio/mpeg', format: 'mp3', duration_seconds: 90 },
      { index: 1, mime_type: 'audio/mpeg', format: 'mp3', duration_seconds: 90 },
    ],
    chapters: [
      { title: 'Opening', start_seconds: 0, end_seconds: 60 },
      { title: 'Middle', start_seconds: 60, end_seconds: 120 },
      { title: 'Finale', start_seconds: 120, end_seconds: 180 },
    ],
  },
  progress: {
    user_id: 'alice',
    book_id: 'book1',
    current_seconds: 65,
    progress_pct: 0.36,
    is_finished: false,
    updated_at: new Date().toISOString(),
  },
  bookmarks: [
    {
      id: 'bm1',
      user_id: 'alice',
      book_id: 'book1',
      position_seconds: 42,
      note: '',
      created_at: new Date().toISOString(),
    },
  ],
};

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: async () => undefined },
      configurable: true,
    });
  });

  await page.route('**/api/v1/audiobooks/book1', (route) =>
    route.fulfill({ json: detail }),
  );
  await page.route('**/api/v1/me/listening-stats/book1', (route) =>
    route.fulfill({
      json: {
        user_id: 'alice',
        book_id: 'book1',
        listened_seconds: 123,
        last_position: 65,
        updated_at: new Date().toISOString(),
      },
    }),
  );
  await page.route('**/api/v1/me/playback-sessions', (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: 'other-session',
            user_id: 'alice',
            book_id: 'book1',
            device_id: 'phone',
            play_method: 'directplay',
            media_player: 'Mobile',
            start_time: 0,
            current_time: 40,
            started_at: new Date().toISOString(),
            last_update: new Date().toISOString(),
          },
        ],
      },
    }),
  );
  await page.route('**/api/v1/audiobooks/book1/playback-session', (route) =>
    route.fulfill({ status: 201, json: { id: 'web-session', book_id: 'book1', current_seconds: 65 } }),
  );
  await page.route('**/api/v1/playback-sessions/**/close', (route) =>
    route.fulfill({ status: 204 }),
  );
  await page.route('**/api/v1/playback-sessions/**', (route) =>
    route.fulfill({ status: 200, json: { ok: true } }),
  );
  await page.route('**/api/v1/audiobooks/book1/bookmarks', (route) =>
    route.fulfill({ status: 201, json: { id: 'bm2' } }),
  );
  await page.route('**/api/v1/audiobooks/book1/files/*/stream', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'audio/mpeg',
      body: Buffer.from([0, 1, 2, 3]),
    }),
  );
});

test('reader page exposes persistent player controls and smoke flows', async ({ page }) => {
  await page.goto('/audiobook/book1?t=75');

  await expect(page.getByRole('heading', { name: 'Test Audiobook' })).toBeVisible();
  await expect(page.getByRole('button', { name: /Middle/ })).toBeVisible();

  await page.getByPlaceholder('Filter chapters').fill('final');
  await expect(page.getByRole('button', { name: /Finale/ })).toBeVisible();
  await expect(page.getByRole('button', { name: /Opening/ })).toBeHidden();

  await page.getByRole('button', { name: /Finale/ }).click();
  await page.getByRole('button', { name: 'Bookmark' }).first().click();
  await page.getByRole('button', { name: 'Clip link' }).click();

  await page.getByRole('button', { name: 'Download' }).click();
  await expect(page.getByRole('button', { name: /Downloaded|Use download/ })).toBeVisible();

  await page.getByText('Diagnostics').click();
  await expect(page.getByText('Session: web-session')).toBeVisible();
  await expect(page.getByText('Source: Downloaded')).toBeVisible();
  await expect(page.getByText('Listened:')).toBeVisible();

  await expect(page.getByText('Active sessions')).toBeVisible();
  await page
    .locator('section')
    .filter({ hasText: 'Active sessions' })
    .getByRole('button', { name: 'Stop' })
    .click({ force: true });
});
