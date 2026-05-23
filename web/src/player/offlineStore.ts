import { authedFetch } from '@/api/client';

const DB_NAME = 'silo-audiobook-offline';
const STORE = 'files';

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = () => req.result.createObjectStore(STORE);
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

function key(bookId: string, fileIndex: number): string {
  return `${bookId}:${fileIndex}`;
}

export async function getOfflineBlob(bookId: string, fileIndex: number): Promise<Blob | undefined> {
  const db = await openDB();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE, 'readonly');
    const req = tx.objectStore(STORE).get(key(bookId, fileIndex));
    req.onsuccess = () => resolve(req.result as Blob | undefined);
    req.onerror = () => reject(req.error);
  });
}

export async function saveOfflineBlob(bookId: string, fileIndex: number, blob: Blob): Promise<void> {
  const db = await openDB();
  await new Promise<void>((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite');
    tx.objectStore(STORE).put(blob, key(bookId, fileIndex));
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}

export async function deleteOfflineBlob(bookId: string, fileIndex: number): Promise<void> {
  const db = await openDB();
  await new Promise<void>((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite');
    tx.objectStore(STORE).delete(key(bookId, fileIndex));
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}

export async function offlineBlobSize(bookId: string, fileIndex: number): Promise<number> {
  const blob = await getOfflineBlob(bookId, fileIndex);
  return blob?.size ?? 0;
}

export async function downloadOfflineFile(
  bookId: string,
  fileIndex: number,
  url: string,
): Promise<void> {
  const response = await authedFetch(url);
  if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => '')}`);
  await saveOfflineBlob(bookId, fileIndex, await response.blob());
}

export async function hasOfflineBlob(bookId: string, fileIndex: number): Promise<boolean> {
  return !!(await getOfflineBlob(bookId, fileIndex));
}
