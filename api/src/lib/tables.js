// Storage access for the agents API. Handlers talk to a small "store"
// interface so the same code runs against Azure Table/Blob storage and against
// the in-memory store used by unit tests and the offline harness.
//
// store.table(name) -> { get, create, createBatch, replace, merge, upsertMerge, delete, list }
//   createBatch(entities): one all-or-nothing insert of <= 100 entities that
//   share a partition key (an entity group transaction); a row that already
//   exists fails the whole batch with 409.
// store.blobs       -> { put, get, delete }
// store.cache       -> Map for per-process caches (machine tokens, months, accounts)
//
// AGENTS_TABLE_PREFIX (letters/digits) isolates tests: tables become
// <prefix><name> and the blob container <prefix>prompts, both created lazily.
// Tables in CREATED_ON_USE are created lazily without a prefix too, so a
// table added after production was provisioned needs no manual step.
import { TableClient, TableServiceClient, odata } from '@azure/data-tables'
import { BlobServiceClient } from '@azure/storage-blob'

export const TABLES = ['events', 'eventindex', 'rollups', 'dirtydays', 'machines', 'invites', 'accounts', 'prompts', 'fleetsecrets']
export const CREATED_ON_USE = new Set(['fleetsecrets'])
export const PROMPTS_CONTAINER = 'prompts'

export function statusOf(err) {
  return err?.statusCode ?? err?.status ?? 0
}

export function isStatus(err, ...codes) {
  return codes.includes(statusOf(err))
}

function httpError(statusCode, message) {
  const err = new Error(message)
  err.statusCode = statusCode
  return err
}

// Table numbers come back as plain numbers, except Int64 which the SDK
// returns as { value, type }.
export function num(v) {
  if (v == null) return 0
  if (typeof v === 'object' && 'value' in v) return Number(v.value)
  const n = Number(v)
  return Number.isFinite(n) ? n : 0
}

function checkPrefix(prefix) {
  if (prefix && !/^[A-Za-z][A-Za-z0-9]*$/.test(prefix)) {
    throw new Error('AGENTS_TABLE_PREFIX must be letters/digits and start with a letter')
  }
  return prefix || ''
}

// Strip read-only metadata before writing an entity back.
function clean(entity) {
  const out = {}
  for (const [k, v] of Object.entries(entity)) {
    if (k === 'etag' || k === 'timestamp' || k.startsWith('odata.')) continue
    out[k] = v
  }
  return out
}

// ---------- Azure ----------

export function azureStore({ connectionString, prefix } = {}) {
  if (!connectionString) throw new Error('AGENTS_STORAGE_CONNECTION_STRING is not set')
  prefix = checkPrefix(prefix)
  const lazyCreate = prefix !== ''
  const clients = new Map()
  let blobContainer = null
  let blobReady = null

  function table(name) {
    const full = prefix + name
    let entry = clients.get(full)
    if (!entry) {
      const client = TableClient.fromConnectionString(connectionString, full)
      const ready = lazyCreate || CREATED_ON_USE.has(name) ? client.createTable().catch((err) => {
        if (isStatus(err, 409)) return
        // Not cached: the next call tries to create it again.
        clients.delete(full)
        throw err
      }) : Promise.resolve()
      entry = { client, ready }
      clients.set(full, entry)
    }
    const { client, ready } = entry
    return {
      name: full,
      async get(pk, rk) {
        await ready
        try {
          return await client.getEntity(pk, rk)
        } catch (err) {
          if (isStatus(err, 404)) return null
          throw err
        }
      },
      async create(entity) {
        await ready
        const res = await client.createEntity(clean(entity))
        return res.etag
      },
      async createBatch(entities) {
        await ready
        await client.submitTransaction(entities.map((e) => ['create', clean(e)]))
      },
      async replace(entity, etag) {
        await ready
        const res = await client.updateEntity(clean(entity), 'Replace', { etag })
        return res.etag
      },
      async merge(entity, etag = '*') {
        await ready
        const res = await client.updateEntity(clean(entity), 'Merge', { etag })
        return res.etag
      },
      async upsertMerge(entity) {
        await ready
        const res = await client.upsertEntity(clean(entity), 'Merge')
        return res.etag
      },
      async delete(pk, rk) {
        await ready
        try {
          await client.deleteEntity(pk, rk)
        } catch (err) {
          if (!isStatus(err, 404)) throw err
        }
      },
      async list(pk, { rkGte, rkLte, select } = {}) {
        await ready
        let filter = odata`PartitionKey eq ${pk}`
        if (rkGte != null) filter += ' and ' + odata`RowKey ge ${rkGte}`
        if (rkLte != null) filter += ' and ' + odata`RowKey le ${rkLte}`
        const queryOptions = { filter }
        if (select) queryOptions.select = ['PartitionKey', 'RowKey', ...select]
        const out = []
        for await (const e of client.listEntities({ queryOptions })) out.push(e)
        return out
      },
    }
  }

  function container() {
    if (!blobContainer) {
      const svc = BlobServiceClient.fromConnectionString(connectionString)
      blobContainer = svc.getContainerClient((prefix + PROMPTS_CONTAINER).toLowerCase())
      blobReady = lazyCreate ? blobContainer.createIfNotExists().then(() => {}) : Promise.resolve()
    }
    return blobContainer
  }

  const blobs = {
    async put(name, text) {
      const c = container()
      await blobReady
      const body = Buffer.from(text, 'utf8')
      await c.getBlockBlobClient(name).upload(body, body.length, {
        blobHTTPHeaders: { blobContentType: 'text/plain; charset=utf-8' },
      })
    },
    async get(name) {
      const c = container()
      await blobReady
      try {
        const buf = await c.getBlockBlobClient(name).downloadToBuffer()
        return buf.toString('utf8')
      } catch (err) {
        if (isStatus(err, 404)) return null
        throw err
      }
    },
    async delete(name) {
      const c = container()
      await blobReady
      await c.getBlockBlobClient(name).deleteIfExists()
    },
  }

  return { kind: 'azure', prefix, table, blobs, cache: new Map() }
}

// Deletes every table and the blob container created under a test prefix.
// Refuses to run without a prefix so production data can never be dropped.
export async function dropPrefixed(connectionString, prefix) {
  prefix = checkPrefix(prefix)
  if (!prefix) throw new Error('refusing to drop unprefixed tables')
  const svc = TableServiceClient.fromConnectionString(connectionString)
  const dropped = []
  for await (const t of svc.listTables({ queryOptions: { filter: odata`TableName ge ${prefix} and TableName lt ${prefix + '{'}` } })) {
    if (!t.name?.startsWith(prefix)) continue
    await svc.deleteTable(t.name)
    dropped.push(t.name)
  }
  const container = BlobServiceClient.fromConnectionString(connectionString)
    .getContainerClient((prefix + PROMPTS_CONTAINER).toLowerCase())
  const res = await container.deleteIfExists()
  if (res.succeeded) dropped.push(container.containerName)
  return dropped
}

// ---------- In memory ----------

export function memoryStore() {
  const tables = new Map()
  const blobMap = new Map()
  let etagSeq = 0
  const nextEtag = () => `W/"m${++etagSeq}"`
  const key = (pk, rk) => `${pk}\u0000${rk}`
  const copy = (v) => structuredClone(v)

  function table(name) {
    let rows = tables.get(name)
    if (!rows) {
      rows = new Map()
      tables.set(name, rows)
    }
    const read = (row) => ({ ...copy(row.entity), etag: row.etag })
    return {
      name,
      async get(pk, rk) {
        const row = rows.get(key(pk, rk))
        return row ? read(row) : null
      },
      async create(entity) {
        const k = key(entity.partitionKey, entity.rowKey)
        if (rows.has(k)) throw httpError(409, 'EntityAlreadyExists')
        const etag = nextEtag()
        rows.set(k, { entity: copy(clean(entity)), etag })
        return etag
      },
      async createBatch(entities) {
        if (entities.length > 100) throw httpError(400, 'InvalidInput')
        if (new Set(entities.map((e) => e.partitionKey)).size > 1) throw httpError(400, 'CommandsInBatchActOnDifferentPartitions')
        const keys = entities.map((e) => key(e.partitionKey, e.rowKey))
        if (new Set(keys).size !== keys.length) throw httpError(400, 'InvalidDuplicateRow')
        if (keys.some((k) => rows.has(k))) throw httpError(409, 'EntityAlreadyExists')
        entities.forEach((e, i) => rows.set(keys[i], { entity: copy(clean(e)), etag: nextEtag() }))
      },
      async replace(entity, etag) {
        const k = key(entity.partitionKey, entity.rowKey)
        const row = rows.get(k)
        if (!row) throw httpError(404, 'ResourceNotFound')
        if (etag !== '*' && etag !== row.etag) throw httpError(412, 'UpdateConditionNotSatisfied')
        const next = nextEtag()
        rows.set(k, { entity: copy(clean(entity)), etag: next })
        return next
      },
      async merge(entity, etag = '*') {
        const k = key(entity.partitionKey, entity.rowKey)
        const row = rows.get(k)
        if (!row) throw httpError(404, 'ResourceNotFound')
        if (etag !== '*' && etag !== row.etag) throw httpError(412, 'UpdateConditionNotSatisfied')
        const next = nextEtag()
        rows.set(k, { entity: { ...row.entity, ...copy(clean(entity)) }, etag: next })
        return next
      },
      async upsertMerge(entity) {
        const k = key(entity.partitionKey, entity.rowKey)
        const row = rows.get(k)
        const next = nextEtag()
        rows.set(k, { entity: { ...(row?.entity ?? {}), ...copy(clean(entity)) }, etag: next })
        return next
      },
      async delete(pk, rk) {
        rows.delete(key(pk, rk))
      },
      async list(pk, { rkGte, rkLte } = {}) {
        const out = []
        for (const row of rows.values()) {
          const e = row.entity
          if (e.partitionKey !== pk) continue
          if (rkGte != null && e.rowKey < rkGte) continue
          if (rkLte != null && e.rowKey > rkLte) continue
          out.push(read(row))
        }
        return out.sort((a, b) => (a.rowKey < b.rowKey ? -1 : a.rowKey > b.rowKey ? 1 : 0))
      },
    }
  }

  const blobs = {
    async put(name, text) {
      blobMap.set(name, text)
    },
    async get(name) {
      return blobMap.has(name) ? blobMap.get(name) : null
    },
    async delete(name) {
      blobMap.delete(name)
    },
  }

  return { kind: 'memory', prefix: '', table, blobs, cache: new Map(), _tables: tables, _blobs: blobMap }
}

// ---------- Default store for the Functions host ----------

let defaultStore = null

export function getDefaultStore(env = process.env) {
  if (!defaultStore) {
    defaultStore = azureStore({
      connectionString: env.AGENTS_STORAGE_CONNECTION_STRING,
      prefix: env.AGENTS_TABLE_PREFIX,
    })
  }
  return defaultStore
}
