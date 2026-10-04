// Prompt encryption at rest (SPEC "Prompt encryption"): AES-256-GCM, 12-byte
// random IV, AAD = prompt id, stored as base64(iv || ciphertext || tag).
import { createCipheriv, createDecipheriv, randomBytes } from 'node:crypto'

const IV_BYTES = 12
const TAG_BYTES = 16

export function parseKey(b64) {
  if (!b64) throw new Error('PROMPT_ENC_KEY is not set')
  const key = Buffer.from(b64, 'base64')
  if (key.length !== 32) throw new Error('PROMPT_ENC_KEY must be base64 of 32 bytes')
  return key
}

export function encryptJson(key, id, obj) {
  const iv = randomBytes(IV_BYTES)
  const cipher = createCipheriv('aes-256-gcm', key, iv)
  cipher.setAAD(Buffer.from(id, 'utf8'))
  const ct = Buffer.concat([cipher.update(JSON.stringify(obj), 'utf8'), cipher.final()])
  return Buffer.concat([iv, ct, cipher.getAuthTag()]).toString('base64')
}

// Throws if the key, the id (AAD) or the ciphertext does not match.
export function decryptJson(key, id, enc) {
  const buf = Buffer.from(enc, 'base64')
  if (buf.length < IV_BYTES + TAG_BYTES) throw new Error('ciphertext too short')
  const iv = buf.subarray(0, IV_BYTES)
  const tag = buf.subarray(buf.length - TAG_BYTES)
  const ct = buf.subarray(IV_BYTES, buf.length - TAG_BYTES)
  const decipher = createDecipheriv('aes-256-gcm', key, iv)
  decipher.setAAD(Buffer.from(id, 'utf8'))
  decipher.setAuthTag(tag)
  const pt = Buffer.concat([decipher.update(ct), decipher.final()])
  return JSON.parse(pt.toString('utf8'))
}
