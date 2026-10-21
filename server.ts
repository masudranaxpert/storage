import express, { Request, Response } from 'express';
import fs from 'fs';
import path from 'path';
import crypto from 'crypto';

const app = express();
const PORT = Number(process.env.PORT) || 3000;
const CURRENT_VERSION = 'v2.8.0';
const CLUSTER_SECRET = process.env.STREAM_SECRET || 'stream-control-plane-session-2026';

app.use(express.json({ limit: '50mb' }));
app.use(express.urlencoded({ extended: true }));

// CORS Middleware matching pkg/web/routes.go
app.use((req, res, next) => {
  res.setHeader('Access-Control-Allow-Origin', '*');
  res.setHeader('Access-Control-Allow-Methods', 'GET, POST, PUT, PATCH, DELETE, OPTIONS, HEAD');
  res.setHeader(
    'Access-Control-Allow-Headers',
    'Content-Type, Authorization, Range, Origin, Accept, X-Cluster-Secret, X-API-Key'
  );
  res.setHeader('Access-Control-Expose-Headers', 'Content-Length, Content-Range, Accept-Ranges');
  if (req.method === 'OPTIONS') {
    res.status(200).end();
    return;
  }
  next();
});

// Static Assets
const staticDir = path.join(process.cwd(), 'web', 'static');
const templateDir = path.join(process.cwd(), 'web', 'templates');
app.use('/static', express.static(staticDir));

// ============================================================================
// Go html/template compatible renderer for web/templates/*/*.html
// ============================================================================
function loadTemplateMap(): Map<string, string> {
  const map = new Map<string, string>();
  const subdirs = ['layouts', 'components', 'pages'];
  for (const sub of subdirs) {
    const dirPath = path.join(templateDir, sub);
    if (!fs.existsSync(dirPath)) continue;
    for (const file of fs.readdirSync(dirPath)) {
      if (file.endsWith('.html')) {
        map.set(file, fs.readFileSync(path.join(dirPath, file), 'utf-8'));
      }
    }
  }
  return map;
}

function evalCondition(expr: string, ctx: { CurrentPage: string; NeedsAuth: boolean }): boolean {
  const trimmed = expr.trim();
  if (trimmed === '.NeedsAuth') return Boolean(ctx.NeedsAuth);
  if (trimmed === '.CurrentPage') return Boolean(ctx.CurrentPage);

  const eqMatch = trimmed.match(/^eq\s+\.CurrentPage\s+"([^"]*)"$/);
  if (eqMatch) {
    return ctx.CurrentPage === eqMatch[1];
  }

  if (trimmed.startsWith('or ')) {
    const eqRegex = /\(eq\s+\.CurrentPage\s+"([^"]*)"\)/g;
    let m: RegExpExecArray | null;
    while ((m = eqRegex.exec(trimmed)) !== null) {
      if (ctx.CurrentPage === m[1]) return true;
    }
    return false;
  }
  return false;
}

function renderGoTemplate(
  templateName: string,
  ctx: { CurrentPage: string; NeedsAuth: boolean },
  templates: Map<string, string>
): string {
  let content = templates.get(templateName) || '';

  // 1. Expand {{template "name.html" .}} recursively (up to 5 passes)
  for (let pass = 0; pass < 5; pass++) {
    if (!content.includes('{{template ')) break;
    content = content.replace(/\{\{template\s+"([^"]+)"\s+\.\}\}/g, (_match, subName) => {
      return templates.get(subName) || '';
    });
  }

  // 2. Evaluate {{if ...}}...{{else}}...{{end}} and {{if ...}}...{{end}}
  // Process innermost {{if ...}} blocks iteratively
  const ifBlockRegex = /\{\{if\s+([^{}]+)\}\}((?:(?!\{\{if\s)[\s\S])*?)\{\{end\}\}/g;
  for (let pass = 0; pass < 10; pass++) {
    if (!content.includes('{{if ')) break;
    content = content.replace(ifBlockRegex, (_match, condExpr, body) => {
      const cond = evalCondition(condExpr, ctx);
      const parts = body.split('{{else}}');
      if (parts.length > 1) {
        return cond ? parts[0] : parts.slice(1).join('{{else}}');
      }
      return cond ? body : '';
    });
  }

  // 3. Replace value placeholders
  content = content.replace(/\{\{\.CurrentPage\}\}/g, ctx.CurrentPage || 'dashboard');
  return content;
}

// ============================================================================
// In-Memory State (Seeded for instant dashboard operation)
// ============================================================================
interface DiskStat {
  path: string;
  fs_type: string;
  disk_type: string;
  total_bytes: number;
  free_bytes: number;
  used_bytes: number;
  used_percent: number;
}

interface NodeRecord {
  metrics: {
    node_id: string;
    hostname: string;
    os: string;
    platform: string;
    ips: string[];
    uptime_sec: number;
    disks: DiskStat[];
    memory: {
      total_bytes: number;
      available_bytes: number;
      used_bytes: number;
      used_percent: number;
    };
    cpu: {
      cores: number;
      model_name: string;
      used_percent: number;
    };
    capabilities: {
      has_ffmpeg: boolean;
      has_aria2c: boolean;
      has_rclone: boolean;
      agent_port: number;
      version: string;
    };
    media_path: string;
    reported_at: string;
  };
  status: 'online' | 'stale' | 'offline' | 'connecting';
  last_seen: string;
  custom_label?: string;
  allocated_max_bytes?: number;
  cors_allowed_origins?: string;
}

const GB = 1024 * 1024 * 1024;
const TB = 1024 * GB;

const nodesMap = new Map<string, NodeRecord>([
  [
    'vps-fra-nvme-01',
    {
      metrics: {
        node_id: 'vps-fra-nvme-01',
        hostname: 'fra-edge-nvme-01',
        os: 'linux',
        platform: 'ubuntu-24.04',
        ips: ['198.51.100.14'],
        uptime_sec: 1425600,
        disks: [
          {
            path: '/mnt/nvme0',
            fs_type: 'ext4',
            disk_type: 'NVME',
            total_bytes: 2 * TB,
            free_bytes: 1.35 * TB,
            used_bytes: 0.65 * TB,
            used_percent: 32.5,
          },
        ],
        memory: {
          total_bytes: 32 * GB,
          available_bytes: 22.4 * GB,
          used_bytes: 9.6 * GB,
          used_percent: 30.0,
        },
        cpu: {
          cores: 16,
          model_name: 'AMD EPYC 9354P 32-Core Processor',
          used_percent: 24.5,
        },
        capabilities: {
          has_ffmpeg: true,
          has_aria2c: true,
          has_rclone: true,
          agent_port: 2052,
          version: CURRENT_VERSION,
        },
        media_path: '/mnt/nvme0/stream/media',
        reported_at: new Date().toISOString(),
      },
      status: 'online',
      last_seen: new Date().toISOString(),
      custom_label: 'Frankfurt Hot NVMe Edge',
    },
  ],
  [
    'vps-ams-ssd-02',
    {
      metrics: {
        node_id: 'vps-ams-ssd-02',
        hostname: 'ams-warm-ssd-02',
        os: 'linux',
        platform: 'debian-12',
        ips: ['198.51.100.28'],
        uptime_sec: 984200,
        disks: [
          {
            path: '/mnt/ssd0',
            fs_type: 'ext4',
            disk_type: 'SSD',
            total_bytes: 4 * TB,
            free_bytes: 2.8 * TB,
            used_bytes: 1.2 * TB,
            used_percent: 30.0,
          },
        ],
        memory: {
          total_bytes: 16 * GB,
          available_bytes: 11.2 * GB,
          used_bytes: 4.8 * GB,
          used_percent: 30.0,
        },
        cpu: {
          cores: 8,
          model_name: 'AMD Ryzen 9 7950X',
          used_percent: 18.2,
        },
        capabilities: {
          has_ffmpeg: true,
          has_aria2c: true,
          has_rclone: true,
          agent_port: 2052,
          version: CURRENT_VERSION,
        },
        media_path: '/mnt/ssd0/stream/media',
        reported_at: new Date().toISOString(),
      },
      status: 'online',
      last_seen: new Date().toISOString(),
      custom_label: 'Amsterdam Warm SSD Pool',
    },
  ],
  [
    'vps-hel-hdd-03',
    {
      metrics: {
        node_id: 'vps-hel-hdd-03',
        hostname: 'hel-cold-storage-03',
        os: 'linux',
        platform: 'debian-12',
        ips: ['203.0.113.42'],
        uptime_sec: 2811400,
        disks: [
          {
            path: '/mnt/hdd0',
            fs_type: 'xfs',
            disk_type: 'HDD',
            total_bytes: 16 * TB,
            free_bytes: 11.6 * TB,
            used_bytes: 4.4 * TB,
            used_percent: 27.5,
          },
        ],
        memory: {
          total_bytes: 16 * GB,
          available_bytes: 12.8 * GB,
          used_bytes: 3.2 * GB,
          used_percent: 20.0,
        },
        cpu: {
          cores: 8,
          model_name: 'Intel Xeon Gold 6338',
          used_percent: 12.4,
        },
        capabilities: {
          has_ffmpeg: true,
          has_aria2c: true,
          has_rclone: true,
          agent_port: 2052,
          version: CURRENT_VERSION,
        },
        media_path: '/mnt/hdd0/stream/media',
        reported_at: new Date().toISOString(),
      },
      status: 'online',
      last_seen: new Date().toISOString(),
      custom_label: 'Helsinki High-Capacity Vault',
    },
  ],
]);

interface NodeConfig {
  node_id: string;
  custom_label?: string;
  allocated_max_bytes: number;
  enabled: boolean;
  coordinator_url?: string;
  ssh_host?: string;
  ssh_port?: number;
  ssh_user?: string;
  ssh_password?: string;
  ssh_use_sudo?: boolean;
  cors_allowed_origins?: string;
  processing_enabled: boolean;
  reserved_cpu_cores: number;
  reserved_ram_bytes: number;
  preferred_disk_type: string;
  reserved_storage_bytes: number;
  max_concurrent_ingests: number;
  excluded_drives?: string[];
}

const nodeConfigs = new Map<string, NodeConfig>([
  [
    'vps-fra-nvme-01',
    {
      node_id: 'vps-fra-nvme-01',
      custom_label: 'Frankfurt Hot NVMe Edge',
      allocated_max_bytes: 0,
      enabled: true,
      ssh_host: '198.51.100.14',
      ssh_port: 22,
      ssh_user: 'root',
      cors_allowed_origins: '*',
      processing_enabled: true,
      reserved_cpu_cores: 4,
      reserved_ram_bytes: 4 * GB,
      preferred_disk_type: 'NVME',
      reserved_storage_bytes: 50 * GB,
      max_concurrent_ingests: 4,
      excluded_drives: [],
    },
  ],
  [
    'vps-ams-ssd-02',
    {
      node_id: 'vps-ams-ssd-02',
      custom_label: 'Amsterdam Warm SSD Pool',
      allocated_max_bytes: 0,
      enabled: true,
      ssh_host: '198.51.100.28',
      ssh_port: 22,
      ssh_user: 'root',
      cors_allowed_origins: '*',
      processing_enabled: true,
      reserved_cpu_cores: 2,
      reserved_ram_bytes: 2 * GB,
      preferred_disk_type: 'SSD',
      reserved_storage_bytes: 40 * GB,
      max_concurrent_ingests: 2,
      excluded_drives: [],
    },
  ],
  [
    'vps-hel-hdd-03',
    {
      node_id: 'vps-hel-hdd-03',
      custom_label: 'Helsinki High-Capacity Vault',
      allocated_max_bytes: 0,
      enabled: true,
      ssh_host: '203.0.113.42',
      ssh_port: 22,
      ssh_user: 'root',
      cors_allowed_origins: '*',
      processing_enabled: false,
      reserved_cpu_cores: 0,
      reserved_ram_bytes: 0,
      preferred_disk_type: 'HDD',
      reserved_storage_bytes: 0,
      max_concurrent_ingests: 2,
      excluded_drives: [],
    },
  ],
]);

interface TierBlock {
  node_id: string;
  path: string;
  disk_type: string;
  quota_bytes: number;
  public_host?: string;
  total_bytes?: number;
  free_bytes?: number;
}

interface TierLifecycle {
  speed_rank: number;
  high_watermark: number;
  low_watermark: number;
}

interface Tier {
  id: number;
  name: string;
  mediums: string[];
  default: boolean;
  system: boolean;
  public_host?: string;
  blocks: TierBlock[];
  lifecycle: TierLifecycle;
  created_at: string;
}

const tiersMap = new Map<number, Tier>([
  [
    0,
    {
      id: 0,
      name: 'Tier 0 · Hot (NVMe)',
      mediums: ['NVME'],
      default: false,
      system: true,
      blocks: [
        {
          node_id: 'vps-fra-nvme-01',
          path: '/mnt/nvme0',
          disk_type: 'NVME',
          quota_bytes: 0,
        },
      ],
      lifecycle: { speed_rank: 0, high_watermark: 90, low_watermark: 75 },
      created_at: new Date(Date.now() - 14 * 86400 * 1000).toISOString(),
    },
  ],
  [
    1,
    {
      id: 1,
      name: 'Tier 1 · Warm (SSD)',
      mediums: ['SSD'],
      default: false,
      system: true,
      blocks: [
        {
          node_id: 'vps-ams-ssd-02',
          path: '/mnt/ssd0',
          disk_type: 'SSD',
          quota_bytes: 0,
        },
      ],
      lifecycle: { speed_rank: 1, high_watermark: 90, low_watermark: 75 },
      created_at: new Date(Date.now() - 14 * 86400 * 1000).toISOString(),
    },
  ],
  [
    2,
    {
      id: 2,
      name: 'Tier 2 · Cold (HDD)',
      mediums: ['HDD'],
      default: true,
      system: true,
      blocks: [
        {
          node_id: 'vps-hel-hdd-03',
          path: '/mnt/hdd0',
          disk_type: 'HDD',
          quota_bytes: 0,
        },
      ],
      lifecycle: { speed_rank: 2, high_watermark: 90, low_watermark: 75 },
      created_at: new Date(Date.now() - 14 * 86400 * 1000).toISOString(),
    },
  ],
]);

let flowPolicy = {
  version: 1,
  ingest: [2, 1, 0],
  edges: [
    {
      id: 'e_a1b2c3d4',
      kind: 'promote',
      from: 2,
      to: 1,
      trigger: { heat_at_least: 3.0 },
      enabled: true,
    },
    {
      id: 'e_e5f6a7b8',
      kind: 'promote',
      from: 1,
      to: 0,
      trigger: { heat_at_least: 8.0 },
      enabled: true,
    },
    {
      id: 'e_c9d0e1f2',
      kind: 'demote',
      from: 0,
      to: 1,
      trigger: { idle_days: 7, heat_below: 2.0, on_watermark: true },
      enabled: true,
    },
    {
      id: 'e_3456789a',
      kind: 'demote',
      from: 1,
      to: 2,
      trigger: { idle_days: 14, heat_below: 1.0, on_watermark: true },
      enabled: true,
    },
  ],
  half_life_hours: 24,
  tick_seconds: 60,
  max_tasks_per_tick: 20,
  expire_acknowledged: false,
  updated_at: new Date().toISOString(),
};

interface BucketRecord {
  id: string;
  name: string;
  provider: string;
  endpoint: string;
  region: string;
  access_key: string;
  secret_key: string;
  public_host?: string;
  used_bytes: number;
  object_count: number;
  last_synced_at: string;
  created_at: string;
}

const bucketsMap = new Map<string, BucketRecord>([
  [
    'bkt_r2_archive01',
    {
      id: 'bkt_r2_archive01',
      name: 'stream-cold-archive',
      provider: 'r2',
      endpoint: 'https://f8c2910a.r2.cloudflarestorage.com',
      region: 'auto',
      access_key: 'AKIAIOSFODNN7EXAMPLE',
      secret_key: 'wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY',
      public_host: 'cdn-r2.streammesh.dev',
      used_bytes: Math.round(1.85 * TB),
      object_count: 412,
      last_synced_at: new Date(Date.now() - 600 * 1000).toISOString().replace('T', ' ').slice(0, 19),
      created_at: new Date(Date.now() - 10 * 86400 * 1000).toISOString().replace('T', ' ').slice(0, 19),
    },
  ],
]);

interface MediaItem {
  key: string;
  state: string;
  filename: string;
  size_bytes: number;
  mime_type: string;
  created_by: string;
  progress_percent: number;
  speed?: string;
  error?: string;
  stage: string;
  stage_name: string;
  stage_percent: number;
  transferred_bytes: number;
  total_bytes: number;
  source_type: string;
  node_id: string;
  worker_node_id: string;
  placement: {
    tier_id: number;
    tier_label: string;
    node_id: string;
    path: string;
    public_host?: string;
  };
  stream_url: string;
  play_url: string;
  metadata?: Record<string, unknown>;
  score: number;
  total_hits: number;
  active_viewers: number;
  pinned_tier?: number | null;
  pin_until?: string | null;
  last_hit_at: string;
  created_at: string;
  updated_at: string;
}

const filesMap = new Map<string, MediaItem>([
  [
    'HELH5QjXyETgEwUW',
    {
      key: 'HELH5QjXyETgEwUW',
      state: 'completed',
      filename: 'Tears.of.Steel.2012.2160p.UHD.CMAF.mp4',
      size_bytes: Math.round(4.2 * GB),
      mime_type: 'video/mp4',
      created_by: 'Dashboard',
      progress_percent: 100,
      stage: 'ready',
      stage_name: 'Stored & Streamable',
      stage_percent: 100,
      transferred_bytes: Math.round(4.2 * GB),
      total_bytes: Math.round(4.2 * GB),
      source_type: 'url',
      node_id: 'vps-fra-nvme-01',
      worker_node_id: 'vps-fra-nvme-01',
      placement: {
        tier_id: 0,
        tier_label: 'tier0',
        node_id: 'vps-fra-nvme-01',
        path: '/mnt/nvme0',
      },
      stream_url: '/stream/HELH5QjXyETgEwUW.mp4',
      play_url: '/stream/HELH5QjXyETgEwUW',
      metadata: {
        filename: 'Tears.of.Steel.2012.2160p.UHD.CMAF.mp4',
        container: 'mp4',
        duration_sec: 734,
        video_codec: 'hevc',
        width: 3840,
        height: 2160,
        audio_tracks: [
          { index: 0, language: 'eng', title: 'English 5.1 AAC', codec: 'aac', channels: 6, embedded: true },
          { index: 1, language: 'spa', title: 'Spanish Stereo', codec: 'aac', channels: 2, embedded: false },
        ],
        subtitles: [
          { index: 0, language: 'eng', title: 'English SDH', codec: 'webvtt' },
          { index: 1, language: 'fra', title: 'Français', codec: 'webvtt' },
        ],
      },
      score: 14.8,
      total_hits: 38,
      active_viewers: 4,
      pinned_tier: 0,
      pin_until: null,
      last_hit_at: new Date(Date.now() - 180 * 1000).toISOString(),
      created_at: new Date(Date.now() - 5 * 86400 * 1000).toISOString(),
      updated_at: new Date(Date.now() - 3600 * 1000).toISOString(),
    },
  ],
  [
    '9kLm2PqRsT8vWxYz',
    {
      key: '9kLm2PqRsT8vWxYz',
      state: 'completed',
      filename: 'Sintel.2010.1080p.BluRay.x264.mp4',
      size_bytes: Math.round(1.85 * GB),
      mime_type: 'video/mp4',
      created_by: 'Production Ingest Worker',
      progress_percent: 100,
      stage: 'ready',
      stage_name: 'Stored & Streamable',
      stage_percent: 100,
      transferred_bytes: Math.round(1.85 * GB),
      total_bytes: Math.round(1.85 * GB),
      source_type: 'url',
      node_id: 'vps-ams-ssd-02',
      worker_node_id: 'vps-ams-ssd-02',
      placement: {
        tier_id: 1,
        tier_label: 'tier1',
        node_id: 'vps-ams-ssd-02',
        path: '/mnt/ssd0',
      },
      stream_url: '/stream/9kLm2PqRsT8vWxYz.mp4',
      play_url: '/stream/9kLm2PqRsT8vWxYz',
      metadata: {
        filename: 'Sintel.2010.1080p.BluRay.x264.mp4',
        container: 'mp4',
        duration_sec: 888,
        video_codec: 'h264',
        width: 1920,
        height: 1080,
        audio_tracks: [
          { index: 0, language: 'eng', title: 'English Stereo', codec: 'aac', channels: 2, embedded: true },
        ],
        subtitles: [{ index: 0, language: 'eng', title: 'English', codec: 'webvtt' }],
      },
      score: 5.4,
      total_hits: 14,
      active_viewers: 1,
      pinned_tier: null,
      pin_until: null,
      last_hit_at: new Date(Date.now() - 1200 * 1000).toISOString(),
      created_at: new Date(Date.now() - 8 * 86400 * 1000).toISOString(),
      updated_at: new Date(Date.now() - 2 * 86400 * 1000).toISOString(),
    },
  ],
  [
    '3hjMG311oM4LVqYm',
    {
      key: '3hjMG311oM4LVqYm',
      state: 'completed',
      filename: 'Big.Buck.Bunny.1080p.60fps.CMAF.mp4',
      size_bytes: Math.round(920 * 1024 * 1024),
      mime_type: 'video/mp4',
      created_by: 'Dashboard',
      progress_percent: 100,
      stage: 'ready',
      stage_name: 'Stored & Streamable',
      stage_percent: 100,
      transferred_bytes: Math.round(920 * 1024 * 1024),
      total_bytes: Math.round(920 * 1024 * 1024),
      source_type: 'upload',
      node_id: 'vps-hel-hdd-03',
      worker_node_id: 'vps-fra-nvme-01',
      placement: {
        tier_id: 2,
        tier_label: 'tier2',
        node_id: 'vps-hel-hdd-03',
        path: '/mnt/hdd0',
      },
      stream_url: '/stream/3hjMG311oM4LVqYm.mp4',
      play_url: '/stream/3hjMG311oM4LVqYm',
      metadata: {
        filename: 'Big.Buck.Bunny.1080p.60fps.CMAF.mp4',
        container: 'mp4',
        duration_sec: 596,
        video_codec: 'h264',
        width: 1920,
        height: 1080,
        audio_tracks: [
          { index: 0, language: 'eng', title: 'English AAC', codec: 'aac', channels: 2, embedded: true },
        ],
        subtitles: [],
      },
      score: 1.2,
      total_hits: 3,
      active_viewers: 0,
      pinned_tier: null,
      pin_until: null,
      last_hit_at: new Date(Date.now() - 3 * 86400 * 1000).toISOString(),
      created_at: new Date(Date.now() - 12 * 86400 * 1000).toISOString(),
      updated_at: new Date(Date.now() - 12 * 86400 * 1000).toISOString(),
    },
  ],
]);

interface LifecycleTask {
  id: string;
  key: string;
  kind: 'promote' | 'demote' | 'expire' | 'manual';
  edge_id?: string;
  reason: string;
  from: { tier_id: number; tier_label: string; node_id: string; path: string };
  to: { tier_id: number; tier_label: string; node_id: string; path: string };
  state: 'queued' | 'copying' | 'verifying' | 'switching' | 'cleanup' | 'done' | 'failed' | 'cancelled';
  attempts: number;
  error?: string;
  progress: number;
  speed?: string;
  bytes: number;
  created_at: string;
  updated_at: string;
  started_at?: string;
}

const lifecycleTasks = new Map<string, LifecycleTask>([
  [
    'lct_8f91a2b3c4d5',
    {
      id: 'lct_8f91a2b3c4d5',
      key: 'HELH5QjXyETgEwUW',
      kind: 'promote',
      edge_id: 'e_e5f6a7b8',
      reason: 'heat score 14.80 >= 8.00 threshold (Tier 1 -> Tier 0)',
      from: { tier_id: 1, tier_label: 'tier1', node_id: 'vps-ams-ssd-02', path: '/mnt/ssd0' },
      to: { tier_id: 0, tier_label: 'tier0', node_id: 'vps-fra-nvme-01', path: '/mnt/nvme0' },
      state: 'done',
      attempts: 1,
      progress: 100,
      speed: '412.5 MB/s',
      bytes: Math.round(4.2 * GB),
      created_at: new Date(Date.now() - 7200 * 1000).toISOString(),
      started_at: new Date(Date.now() - 7195 * 1000).toISOString(),
      updated_at: new Date(Date.now() - 7180 * 1000).toISOString(),
    },
  ],
  [
    'lct_4c3b2a1908f7',
    {
      id: 'lct_4c3b2a1908f7',
      key: '9kLm2PqRsT8vWxYz',
      kind: 'promote',
      edge_id: 'e_a1b2c3d4',
      reason: 'heat score 5.40 >= 3.00 threshold (Tier 2 -> Tier 1)',
      from: { tier_id: 2, tier_label: 'tier2', node_id: 'vps-hel-hdd-03', path: '/mnt/hdd0' },
      to: { tier_id: 1, tier_label: 'tier1', node_id: 'vps-ams-ssd-02', path: '/mnt/ssd0' },
      state: 'done',
      attempts: 1,
      progress: 100,
      speed: '285.0 MB/s',
      bytes: Math.round(1.85 * GB),
      created_at: new Date(Date.now() - 86400 * 1000).toISOString(),
      started_at: new Date(Date.now() - 86390 * 1000).toISOString(),
      updated_at: new Date(Date.now() - 86380 * 1000).toISOString(),
    },
  ],
]);

const lifecycleAudits = [
  {
    key: '7xYz9AbCdEfGhIjK',
    filename: 'Legacy.Cam.Sample.720p.mp4',
    size: 640 * 1024 * 1024,
    tier: 2,
    node: 'vps-hel-hdd-03',
    path: '/mnt/hdd0',
    edge_id: 'e_expire_demo',
    reason: 'idle 45d >= 30d threshold on cold tier',
    at: new Date(Date.now() - 3 * 86400 * 1000).toISOString(),
  },
];

interface APIKeyRecord {
  key: string;
  prefix: string;
  name: string;
  created_at: string;
  last_used_at?: string;
}

const apiKeysMap = new Map<string, APIKeyRecord>([
  [
    'sm_live_9f8e7d6c5b4a3210fedcba9876543210',
    {
      key: 'sm_live_9f8e7d6c5b4a3210fedcba9876543210',
      prefix: 'sm_live_9f8e...',
      name: 'Production Ingest Worker',
      created_at: new Date(Date.now() - 10 * 86400 * 1000).toISOString(),
      last_used_at: new Date(Date.now() - 600 * 1000).toISOString(),
    },
  ],
]);

interface UserRecord {
  username: string;
  password: string;
  created_at: string;
}

const usersMap = new Map<string, UserRecord>([
  [
    'admin',
    {
      username: 'admin',
      password: 'adminpassword',
      created_at: new Date(Date.now() - 30 * 86400 * 1000).toISOString(),
    },
  ],
]);

// ============================================================================
// Helper Computations
// ============================================================================
function computePoolSummary() {
  const nodes = Array.from(nodesMap.values());
  let activeNodes = 0;
  let offlineNodes = 0;
  let totalDrives = 0;
  let totalStorageBytes = 0;
  let freeStorageBytes = 0;
  let usedStorageBytes = 0;
  let totalRAMBytes = 0;
  let availableRAMBytes = 0;
  let totalCPUCores = 0;
  let cpuLoadSum = 0;

  const nvme = { total_bytes: 0, free_bytes: 0, used_bytes: 0, used_percent: 0, drive_count: 0 };
  const ssd = { total_bytes: 0, free_bytes: 0, used_bytes: 0, used_percent: 0, drive_count: 0 };
  const hdd = { total_bytes: 0, free_bytes: 0, used_bytes: 0, used_percent: 0, drive_count: 0 };

  for (const n of nodes) {
    const cfg = nodeConfigs.get(n.metrics.node_id);
    if (cfg) {
      if (cfg.custom_label) n.custom_label = cfg.custom_label;
      n.allocated_max_bytes = cfg.allocated_max_bytes;
      n.cors_allowed_origins = cfg.cors_allowed_origins;
    }
    if (n.status === 'online') activeNodes++;
    else offlineNodes++;

    totalRAMBytes += n.metrics.memory.total_bytes;
    availableRAMBytes += n.metrics.memory.available_bytes;
    totalCPUCores += n.metrics.cpu.cores;
    cpuLoadSum += n.metrics.cpu.used_percent;

    for (const d of n.metrics.disks) {
      totalDrives++;
      totalStorageBytes += d.total_bytes;
      freeStorageBytes += d.free_bytes;
      usedStorageBytes += d.used_bytes;

      const dt = (d.disk_type || '').toUpperCase();
      const bucket = dt === 'NVME' ? nvme : dt === 'SSD' ? ssd : hdd;
      bucket.total_bytes += d.total_bytes;
      bucket.free_bytes += d.free_bytes;
      bucket.used_bytes += d.used_bytes;
      bucket.drive_count++;
    }
  }

  for (const b of [nvme, ssd, hdd]) {
    b.used_percent = b.total_bytes > 0 ? (b.used_bytes / b.total_bytes) * 100 : 0;
  }

  return {
    total_nodes: nodes.length,
    active_nodes: activeNodes,
    offline_nodes: offlineNodes,
    total_drives: totalDrives,
    total_storage_bytes: totalStorageBytes,
    free_storage_bytes: freeStorageBytes,
    used_storage_bytes: usedStorageBytes,
    storage_used_percent: totalStorageBytes > 0 ? (usedStorageBytes / totalStorageBytes) * 100 : 0,
    total_ram_bytes: totalRAMBytes,
    available_ram_bytes: availableRAMBytes,
    total_cpu_cores: totalCPUCores,
    avg_cpu_load_percent: nodes.length > 0 ? cpuLoadSum / nodes.length : 0,
    nvme,
    ssd,
    hdd,
    nodes,
  };
}

function resolveTiers() {
  const tiers = Array.from(tiersMap.values()).sort((a, b) => a.id - b.id);
  const usedByNode = new Map<string, number>();
  for (const f of filesMap.values()) {
    if (f.state === 'completed') {
      const prev = usedByNode.get(f.placement.node_id) || 0;
      usedByNode.set(f.placement.node_id, prev + f.size_bytes);
    }
  }

  return tiers.map((t) => {
    let usableBytes = 0;
    let usedBytes = 0;
    let freeBytes = 0;

    const blockStatuses = t.blocks.map((b) => {
      const node = nodesMap.get(b.node_id);
      const disk = node?.metrics.disks.find((d) => d.path === b.path);
      const online = Boolean(node && node.status === 'online' && disk) || b.node_id.startsWith('bucket:');
      const total = disk ? disk.total_bytes : b.quota_bytes || 100 * TB;
      const free = disk ? disk.free_bytes : b.quota_bytes || 100 * TB;
      const used = usedByNode.get(b.node_id) || (disk ? disk.used_bytes : 0);
      const usable = b.quota_bytes > 0 && b.quota_bytes < free ? b.quota_bytes : free;
      const usedPct = usable > 0 ? Math.min(100, (used / usable) * 100) : 0;

      usableBytes += usable;
      usedBytes += used;
      freeBytes += Math.max(0, usable - used);

      return {
        block: b,
        online,
        total_bytes: total,
        free_bytes: free,
        used_bytes: used,
        usable_bytes: usable,
        used_percent: usedPct,
      };
    });

    return {
      id: t.id,
      name: t.name,
      mediums: t.mediums,
      default: t.default,
      system: t.system,
      public_host: t.public_host,
      blocks: blockStatuses,
      lifecycle: t.lifecycle,
      usable_bytes: usableBytes,
      used_bytes: usedBytes,
      free_bytes: freeBytes,
      created_at: t.created_at,
    };
  });
}

// ============================================================================
// REST API Endpoints
// ============================================================================

app.get('/api/health', (_req, res) => {
  res.json({ status: 'ok' });
});

// Auth endpoints
app.get('/api/auth/status', (_req, res) => {
  res.json({
    needs_setup: false,
    authenticated: true,
    username: 'admin',
  });
});

app.post('/api/auth/setup', (req, res) => {
  const { username = 'admin', password = 'password' } = req.body || {};
  usersMap.set(username, { username, password, created_at: new Date().toISOString() });
  res.json({ status: 'setup_complete', username });
});

app.post('/api/auth/login', (req, res) => {
  const { username = 'admin' } = req.body || {};
  res.json({ status: 'authenticated', username });
});

app.post('/api/auth/logout', (_req, res) => {
  res.json({ status: 'logged_out' });
});

app.post('/api/auth/password', (_req, res) => {
  res.json({ status: 'password_updated' });
});

app.get('/api/auth/users', (_req, res) => {
  const users = Array.from(usersMap.values()).map((u) => ({
    username: u.username,
    created_at: u.created_at,
  }));
  res.json({ status: 'ok', users });
});

app.post('/api/auth/users', (req, res) => {
  const username = String(req.body?.username || '').trim().toLowerCase();
  const password = String(req.body?.password || '');
  if (username.length < 3 || password.length < 6) {
    res.status(400).json({ error: 'username (>=3 chars) and password (>=6 chars) required' });
    return;
  }
  usersMap.set(username, { username, password, created_at: new Date().toISOString() });
  res.json({ status: 'created', username });
});

app.delete('/api/auth/users/:username', (req, res) => {
  const username = req.params.username.toLowerCase();
  if (usersMap.size <= 1) {
    res.status(400).json({ error: 'cannot delete the only remaining administrator account' });
    return;
  }
  usersMap.delete(username);
  res.json({ status: 'deleted', message: 'User deleted successfully' });
});

// Cluster Secret & System Cache
app.get('/api/cluster/secret', (_req, res) => {
  res.json({ cluster_secret: CLUSTER_SECRET });
});

app.get('/api/system/cache', (_req, res) => {
  const limitBytes = 50 * 1024 * 1024;
  const caches = [
    { name: 'play_deduper', entries: 42, bytes: 18432, evicted: 0 },
    { name: 'node_usage_snapshot', entries: nodesMap.size, bytes: 1024, evicted: 0 },
    { name: 'play_hit_buffer', entries: filesMap.size, bytes: 4096, evicted: 0 },
    { name: 'relocation_inflight', entries: 0, bytes: 256, evicted: 0 },
    { name: 'ghost_strike_tracker', entries: 0, bytes: 128, evicted: 0 },
  ];
  const usedBytes = caches.reduce((sum, c) => sum + c.bytes, 0);
  res.json({
    limit_bytes: limitBytes,
    used_bytes: usedBytes,
    total_bytes: usedBytes,
    usage_pct: Number(((usedBytes / limitBytes) * 100).toFixed(1)),
    caches,
  });
});

// Nodes & Pool
app.get('/api/pool', (_req, res) => {
  res.json(computePoolSummary());
});

app.get('/api/nodes', (_req, res) => {
  const list = Array.from(nodesMap.values()).map((n) => {
    const cfg = nodeConfigs.get(n.metrics.node_id);
    if (cfg) {
      if (cfg.custom_label) n.custom_label = cfg.custom_label;
      n.allocated_max_bytes = cfg.allocated_max_bytes;
      n.cors_allowed_origins = cfg.cors_allowed_origins;
    }
    return n;
  });
  res.json(list);
});

app.post('/api/heartbeat', (req, res) => {
  const metrics = req.body;
  if (!metrics || !metrics.node_id) {
    res.status(400).json({ error: 'node_id is required' });
    return;
  }
  const rec: NodeRecord = {
    metrics,
    status: 'online',
    last_seen: new Date().toISOString(),
  };
  nodesMap.set(metrics.node_id, rec);
  res.json({
    status: 'ok',
    record: rec,
    node_id: metrics.node_id,
    latest_version: CURRENT_VERSION,
    download_url: '/download/stream-bundle-linux-amd64.tar.gz',
    update_available: false,
    install_missing_tools: false,
    cors_allowed_origins: '*',
  });
});

app.post('/api/nodes/provision', (req, res) => {
  const { host, user = 'root', node_name } = req.body || {};
  if (!host) {
    res.status(400).json({ error: 'host is required' });
    return;
  }
  const nodeId = node_name || `vps-${String(host).replace(/[^a-zA-Z0-9]/g, '-')}`;
  const rec: NodeRecord = {
    metrics: {
      node_id: nodeId,
      hostname: nodeId,
      os: 'linux',
      platform: 'ubuntu-24.04',
      ips: [String(host)],
      uptime_sec: 120,
      disks: [
        {
          path: '/mnt/storage0',
          fs_type: 'ext4',
          disk_type: 'SSD',
          total_bytes: 2 * TB,
          free_bytes: 1.9 * TB,
          used_bytes: 0.1 * TB,
          used_percent: 5.0,
        },
      ],
      memory: {
        total_bytes: 16 * GB,
        available_bytes: 14 * GB,
        used_bytes: 2 * GB,
        used_percent: 12.5,
      },
      cpu: {
        cores: 8,
        model_name: 'AMD EPYC Virtual Processor',
        used_percent: 8.0,
      },
      capabilities: {
        has_ffmpeg: true,
        has_aria2c: true,
        has_rclone: true,
        agent_port: 2052,
        version: CURRENT_VERSION,
      },
      media_path: '/mnt/storage0/stream/media',
      reported_at: new Date().toISOString(),
    },
    status: 'online',
    last_seen: new Date().toISOString(),
  };
  nodesMap.set(nodeId, rec);
  nodeConfigs.set(nodeId, {
    node_id: nodeId,
    allocated_max_bytes: 0,
    enabled: true,
    ssh_host: host,
    ssh_port: 22,
    ssh_user: user,
    cors_allowed_origins: '*',
    processing_enabled: true,
    reserved_cpu_cores: 2,
    reserved_ram_bytes: 2 * GB,
    preferred_disk_type: 'SSD',
    reserved_storage_bytes: 20 * GB,
    max_concurrent_ingests: 2,
    excluded_drives: [],
  });
  res.json({
    node_name: nodeId,
    host,
    advertise_addr: host,
    logs: [
      `[SSH] Connected to ${user}@${host}:22`,
      `[Bundle] Verified static media binaries (ffmpeg, ffprobe, aria2c, rclone)`,
      `[Systemd] Started stream-agent.service on :2052`,
    ],
  });
});

app.all('/api/nodes/:nodeId*', (req, res) => {
  const rawAfter = req.path.replace(/^\/api\/nodes\//, '');
  const parts = rawAfter.split('/').filter(Boolean);
  const nodeId = parts[0];
  const action = parts[1] || '';

  if (req.method === 'DELETE' && !action) {
    nodesMap.delete(nodeId);
    nodeConfigs.delete(nodeId);
    res.json({ status: 'deleted', remote_stopped: true });
    return;
  }

  const cfg: NodeConfig = nodeConfigs.get(nodeId) || {
    node_id: nodeId,
    allocated_max_bytes: 0,
    enabled: true,
    cors_allowed_origins: '*',
    processing_enabled: true,
    reserved_cpu_cores: 2,
    reserved_ram_bytes: 2 * GB,
    preferred_disk_type: 'SSD',
    reserved_storage_bytes: 20 * GB,
    max_concurrent_ingests: 2,
    excluded_drives: [],
  };

  if (action === 'config' && req.method === 'GET') {
    res.json({ ...cfg, ssh_password: cfg.ssh_password ? '******' : '' });
    return;
  }

  if ((action === 'config' || action === 'edit') && (req.method === 'POST' || req.method === 'PUT')) {
    const body = req.body || {};
    if (body.custom_label !== undefined) {
      cfg.custom_label = String(body.custom_label).trim();
      const node = nodesMap.get(nodeId);
      if (node) node.custom_label = cfg.custom_label;
    }
    if (body.allocated_max_bytes !== undefined) cfg.allocated_max_bytes = Number(body.allocated_max_bytes);
    if (body.enabled !== undefined) cfg.enabled = Boolean(body.enabled);
    if (body.cors_allowed_origins !== undefined) cfg.cors_allowed_origins = String(body.cors_allowed_origins).trim();
    if (body.ssh_host !== undefined) cfg.ssh_host = String(body.ssh_host).trim();
    if (body.ssh_port !== undefined) cfg.ssh_port = Number(body.ssh_port);
    if (body.ssh_user !== undefined) cfg.ssh_user = String(body.ssh_user).trim();
    const node = nodesMap.get(nodeId);
    if (node) {
      node.allocated_max_bytes = cfg.allocated_max_bytes;
      node.cors_allowed_origins = cfg.cors_allowed_origins;
    }
    nodeConfigs.set(nodeId, cfg);
    res.json({ ...cfg, ssh_password: '******' });
    return;
  }

  if (action === 'allocate' && req.method === 'POST') {
    cfg.allocated_max_bytes = Number(req.body?.allocated_max_bytes || 0);
    nodeConfigs.set(nodeId, cfg);
    res.json(cfg);
    return;
  }

  if (action === 'processing' && req.method === 'POST') {
    const body = req.body || {};
    cfg.processing_enabled = Boolean(body.enabled ?? body.processing_enabled);
    if (body.reserved_cpu_cores !== undefined) cfg.reserved_cpu_cores = Number(body.reserved_cpu_cores);
    if (body.reserved_ram_bytes !== undefined) cfg.reserved_ram_bytes = Number(body.reserved_ram_bytes);
    else if (body.reserved_ram_mb !== undefined) cfg.reserved_ram_bytes = Number(body.reserved_ram_mb) * 1024 * 1024;
    if (body.preferred_disk_type || body.preferred_disk) {
      cfg.preferred_disk_type = String(body.preferred_disk || body.preferred_disk_type).toUpperCase();
    }
    if (body.reserved_storage_bytes !== undefined) cfg.reserved_storage_bytes = Number(body.reserved_storage_bytes);
    else if (body.reserved_storage_gb !== undefined) cfg.reserved_storage_bytes = Number(body.reserved_storage_gb) * GB;
    if (body.max_concurrent_ingests !== undefined) cfg.max_concurrent_ingests = Number(body.max_concurrent_ingests);
    nodeConfigs.set(nodeId, cfg);
    res.json(cfg);
    return;
  }

  res.json({ status: 'ok', message: `Action '${action || 'update'}' completed for node '${nodeId}'` });
});

// Storage, Folders, Buckets, Tiers & Processing
app.get('/api/storage/folders', (_req, res) => {
  const results = Array.from(nodesMap.values()).map((n) => {
    const nodeFiles = Array.from(filesMap.values()).filter((f) => f.placement.node_id === n.metrics.node_id);
    const mediaBytes = nodeFiles.reduce((acc, f) => acc + f.size_bytes, 0);
    return {
      node_id: n.metrics.node_id,
      hostname: n.metrics.hostname,
      primary_ip: n.metrics.ips[0] || '',
      ips: n.metrics.ips,
      status: n.status,
      media_size_bytes: mediaBytes,
      media_file_count: nodeFiles.length,
      processing_size_bytes: 0,
      processing_file_count: 0,
      total_stream_bytes: mediaBytes,
      drives: n.metrics.disks.map((d) => ({
        drive_name: d.path,
        mount_point: d.path,
        stream_root: `${d.path}/stream`,
        media_dir: `${d.path}/stream/media`,
        processing_dir: `${d.path}/stream/processing`,
        media_size_bytes: mediaBytes,
        media_file_count: nodeFiles.length,
        processing_size_bytes: 0,
        processing_file_count: 0,
        total_stream_bytes: mediaBytes,
      })),
    };
  });

  for (const b of bucketsMap.values()) {
    const provName = (b.provider || 'r2').toUpperCase();
    results.push({
      node_id: `bucket:${b.id}`,
      hostname: b.name,
      primary_ip: b.endpoint,
      ips: [b.endpoint],
      status: 'online',
      media_size_bytes: b.used_bytes,
      media_file_count: b.object_count,
      processing_size_bytes: 0,
      processing_file_count: 0,
      total_stream_bytes: b.used_bytes,
      drives: [
        {
          drive_name: `${b.name} (${provName})`,
          mount_point: b.name,
          stream_root: b.endpoint,
          media_dir: b.name,
          processing_dir: '',
          media_size_bytes: b.used_bytes,
          media_file_count: b.object_count,
          processing_size_bytes: 0,
          processing_file_count: 0,
          total_stream_bytes: b.used_bytes,
        },
      ],
    });
  }

  res.json({ nodes: results });
});

app.post('/api/storage/clean', (req, res) => {
  const { target = 'scratch' } = req.body || {};
  res.json({ status: 'cleaned', target, freed_bytes: 0, freed_items: 0 });
});

app.post('/api/storage/drive-eligibility', (req, res) => {
  const { node_id, path: drivePath, eligible } = req.body || {};
  const cfg = nodeConfigs.get(node_id);
  if (cfg) {
    const set = new Set(cfg.excluded_drives || []);
    if (eligible) set.delete(drivePath);
    else set.add(drivePath);
    cfg.excluded_drives = Array.from(set);
  }
  res.json({ status: 'ok', node_id, path: drivePath, eligible: Boolean(eligible) });
});

app.get('/api/storage/buckets', (_req, res) => {
  const buckets = Array.from(bucketsMap.values()).map(({ secret_key: _s, ...rest }) => rest);
  res.json({ status: 'ok', buckets });
});

app.post('/api/storage/buckets', (req, res) => {
  const { name, provider = 'r2', endpoint, region = 'auto', access_key, secret_key, public_host } = req.body || {};
  if (!name || !endpoint) {
    res.status(400).json({ error: 'bucket name and endpoint URL are required' });
    return;
  }
  const id = `bkt_${crypto.randomBytes(4).toString('hex')}`;
  const nowStr = new Date().toISOString().replace('T', ' ').slice(0, 19);
  bucketsMap.set(id, {
    id,
    name: String(name).trim(),
    provider: String(provider).toLowerCase(),
    endpoint: String(endpoint).trim(),
    region: String(region).trim(),
    access_key: String(access_key || '').trim(),
    secret_key: String(secret_key || '').trim(),
    public_host: public_host ? String(public_host).trim() : undefined,
    used_bytes: 0,
    object_count: 0,
    last_synced_at: nowStr,
    created_at: nowStr,
  });
  res.json({ status: 'created', id, message: 'Bucket registered successfully' });
});

app.all('/api/storage/buckets/:id*', (req, res) => {
  const rawAfter = req.path.replace(/^\/api\/storage\/buckets\//, '');
  const parts = rawAfter.split('/').filter(Boolean);
  const id = parts[0];
  const sub = parts[1] || '';

  if (sub === 'test' || id === 'test') {
    res.json({ status: 'ok', message: 'Successfully connected to bucket' });
    return;
  }
  const existing = bucketsMap.get(id);
  if (!existing) {
    res.status(404).json({ error: 'bucket not found' });
    return;
  }
  if (sub === 'sync') {
    existing.last_synced_at = new Date().toISOString().replace('T', ' ').slice(0, 19);
    res.json({
      status: 'ok',
      id: existing.id,
      used_bytes: existing.used_bytes,
      object_count: existing.object_count,
      last_synced_at: existing.last_synced_at,
    });
    return;
  }
  if (req.method === 'DELETE') {
    bucketsMap.delete(id);
    res.json({ status: 'deleted', message: `Bucket ${id} removed` });
    return;
  }
  if (req.method === 'PUT' || req.method === 'PATCH') {
    const body = req.body || {};
    if (body.name) existing.name = String(body.name).trim();
    if (body.provider) existing.provider = String(body.provider).toLowerCase();
    if (body.endpoint) existing.endpoint = String(body.endpoint).trim();
    if (body.region !== undefined) existing.region = String(body.region).trim();
    if (body.access_key) existing.access_key = String(body.access_key).trim();
    if (body.public_host !== undefined) existing.public_host = String(body.public_host).trim();
    res.json({ status: 'updated', id: existing.id, message: 'Bucket updated successfully' });
    return;
  }
  res.status(405).json({ error: 'method not allowed' });
});

app.get('/api/processing', (_req, res) => {
  const list = Array.from(nodesMap.values()).map((n) => {
    const cfg = nodeConfigs.get(n.metrics.node_id);
    const enabled = cfg ? cfg.processing_enabled : false;
    const reasons: string[] = [];
    if (!enabled) reasons.push('processing disabled for this node');
    return {
      node_id: n.metrics.node_id,
      eligible: enabled && n.status === 'online',
      reasons,
      profile: {
        node_id: n.metrics.node_id,
        enabled,
        reserved_cpu_cores: cfg?.reserved_cpu_cores || 0,
        reserved_ram_bytes: cfg?.reserved_ram_bytes || 0,
        preferred_disk_type: cfg?.preferred_disk_type || '',
        reserved_storage_bytes: cfg?.reserved_storage_bytes || 0,
        max_concurrent_ingests: cfg?.max_concurrent_ingests || 2,
      },
      free_ram_bytes: n.metrics.memory.available_bytes,
      free_cpu_cores: n.metrics.cpu.cores,
      scratch_free_bytes: n.metrics.disks[0]?.free_bytes || 0,
      disks: n.metrics.disks,
    };
  });
  res.json({ nodes: list });
});

app.post('/api/processing', (req, res) => {
  const enabled = Boolean(req.body?.enabled);
  for (const cfg of nodeConfigs.values()) {
    cfg.processing_enabled = enabled;
  }
  res.json({ status: 'ok', enabled });
});

app.all('/api/tiers*', (req, res) => {
  const subPath = req.path.replace(/^\/api\/tiers\/?/, '');
  if (req.method === 'GET') {
    const statuses = resolveTiers();
    const assigned = new Set<string>();
    for (const ts of statuses) {
      for (const b of ts.blocks) {
        assigned.add(`${b.block.node_id}|${b.block.path}`);
      }
    }
    const excluded: string[] = [];
    for (const [nodeId, cfg] of nodeConfigs.entries()) {
      for (const p of cfg.excluded_drives || []) {
        excluded.push(`${nodeId}|${p}`);
      }
    }
    const excludedSet = new Set(excluded);
    const unassigned: TierBlock[] = [];
    for (const n of nodesMap.values()) {
      for (const d of n.metrics.disks) {
        const key = `${n.metrics.node_id}|${d.path}`;
        if (!assigned.has(key) && !excludedSet.has(key)) {
          unassigned.push({
            node_id: n.metrics.node_id,
            path: d.path,
            disk_type: d.disk_type,
            quota_bytes: 0,
            total_bytes: d.total_bytes,
            free_bytes: d.free_bytes,
          });
        }
      }
    }
    for (const b of bucketsMap.values()) {
      const nodeId = `bucket:${b.id}`;
      const key = `${nodeId}|${b.name}`;
      if (!assigned.has(key)) {
        unassigned.push({
          node_id: nodeId,
          path: b.name,
          disk_type: (b.provider || 'R2').toUpperCase(),
          quota_bytes: 0,
          public_host: b.public_host,
          total_bytes: 100 * TB,
          free_bytes: 100 * TB,
        });
      }
    }
    res.json({
      tiers: statuses,
      unassigned_blocks: unassigned,
      excluded_blocks: excluded,
    });
    return;
  }

  if (req.method === 'PUT' && subPath.endsWith('/lifecycle')) {
    const id = Number(subPath.replace('/lifecycle', ''));
    const tier = tiersMap.get(id);
    if (!tier) {
      res.status(404).json({ error: `tier ${id} not found` });
      return;
    }
    tier.lifecycle = {
      speed_rank: Number(req.body?.speed_rank ?? tier.lifecycle.speed_rank),
      high_watermark: Number(req.body?.high_watermark || 90),
      low_watermark: Number(req.body?.low_watermark || 75),
    };
    res.json(tier);
    return;
  }

  if (req.method === 'POST') {
    const incoming = req.body as Tier;
    let id = Number(incoming.id);
    if (isNaN(id) || id < 0) {
      id = Math.max(0, ...Array.from(tiersMap.keys())) + 1;
    }
    const existing = tiersMap.get(id);
    const updated: Tier = {
      id,
      name: incoming.name || existing?.name || `Tier ${id}`,
      mediums: existing?.system ? existing.mediums : incoming.mediums || ['HDD'],
      default: Boolean(incoming.default),
      system: Boolean(existing?.system),
      public_host: incoming.public_host,
      blocks: Array.isArray(incoming.blocks) ? incoming.blocks : existing?.blocks || [],
      lifecycle: incoming.lifecycle || existing?.lifecycle || { speed_rank: 10 + id, high_watermark: 90, low_watermark: 75 },
      created_at: existing?.created_at || new Date().toISOString(),
    };
    if (updated.default) {
      for (const other of tiersMap.values()) {
        if (other.id !== id) other.default = false;
      }
    }
    tiersMap.set(id, updated);
    res.json(Array.from(tiersMap.values()).sort((a, b) => a.id - b.id));
    return;
  }

  if (req.method === 'DELETE' && subPath) {
    const id = Number(subPath);
    const existing = tiersMap.get(id);
    if (!existing) {
      res.status(404).json({ error: 'tier not found' });
      return;
    }
    if (existing.system) {
      res.status(400).json({ error: `tier${id} is a permanent system tier and cannot be deleted` });
      return;
    }
    tiersMap.delete(id);
    res.json(Array.from(tiersMap.values()).sort((a, b) => a.id - b.id));
    return;
  }

  res.status(405).json({ error: 'method not allowed' });
});

// Flows & Lifecycle
app.get('/api/flows', (_req, res) => {
  const tiers = resolveTiers();
  const stats: Record<number, { file_count: number; hot_count: number }> = {};
  for (const t of tiers) {
    stats[t.id] = { file_count: 0, hot_count: 0 };
  }
  for (const f of filesMap.values()) {
    if (f.state !== 'completed') continue;
    const entry = stats[f.placement.tier_id] || { file_count: 0, hot_count: 0 };
    entry.file_count++;
    if (f.score >= 3.0) entry.hot_count++;
    stats[f.placement.tier_id] = entry;
  }
  res.json({
    policy: flowPolicy,
    tiers,
    stats,
  });
});

app.put('/api/flows', (req, res) => {
  const body = req.body || {};
  flowPolicy = {
    ...flowPolicy,
    ...body,
    version: (flowPolicy.version || 1) + 1,
    updated_at: new Date().toISOString(),
  };
  res.json(flowPolicy);
});

app.all('/api/flows/preview', (_req, res) => {
  res.json({ tasks: [], skipped: [] });
});

app.get('/api/flows/simulate', (req, res) => {
  const sizeBytes = Number(req.query.size_bytes || 0);
  const tiers = resolveTiers();
  for (const tid of flowPolicy.ingest) {
    const tier = tiers.find((t) => t.id === tid);
    if (!tier) continue;
    const block = tier.blocks.find((b) => b.online && b.free_bytes >= sizeBytes);
    if (block) {
      res.json({
        tier_id: tier.id,
        node_id: block.block.node_id,
        path: block.block.path,
        reason: `Selected ${tier.name} on ${block.block.node_id} (${block.block.path}) with sufficient free capacity`,
      });
      return;
    }
  }
  res.status(507).json({ error: 'insufficient storage: no ingest tier fits requested size' });
});

app.get('/api/lifecycle/tasks', (req, res) => {
  const stateFilter = String(req.query.state || '').trim();
  let tasks = Array.from(lifecycleTasks.values());
  if (stateFilter === 'active') {
    tasks = tasks.filter((t) => !['done', 'failed', 'cancelled'].includes(t.state));
  } else if (stateFilter && stateFilter !== 'all') {
    tasks = tasks.filter((t) => t.state === stateFilter);
  }
  res.json({ tasks });
});

app.post('/api/lifecycle/tasks/:id/:action', (req, res) => {
  const task = lifecycleTasks.get(req.params.id);
  if (!task) {
    res.status(404).json({ error: 'task not found' });
    return;
  }
  if (req.params.action === 'cancel') {
    task.state = 'cancelled';
    task.updated_at = new Date().toISOString();
    res.json(task);
    return;
  }
  res.json({ status: 'ok' });
});

app.get('/api/lifecycle/hot', (_req, res) => {
  const hotFiles = Array.from(filesMap.values())
    .map((f) => ({
      key: f.key,
      filename: f.filename,
      score: f.score,
      total_hits: f.total_hits,
      active_viewers: f.active_viewers,
      tier_id: f.placement.tier_id,
      node_id: f.placement.node_id,
      path: f.placement.path,
      pinned_tier: f.pinned_tier,
      pin_until: f.pin_until,
      last_hit_at: f.last_hit_at,
      size_bytes: f.size_bytes,
    }))
    .sort((a, b) => b.score - a.score);
  res.json({ hot_files: hotFiles });
});

app.get('/api/lifecycle/audit', (_req, res) => {
  res.json({ audits: lifecycleAudits });
});

app.post(['/api/lifecycle/hit', '/api/stream/heartbeat', '/api/lifecycle/heartbeat'], (req, res) => {
  const key = req.body?.key || '';
  const item = filesMap.get(key);
  if (item) {
    item.total_hits++;
    item.score = Number((item.score + 1).toFixed(2));
    item.last_hit_at = new Date().toISOString();
  }
  res.json({ status: 'ok', key, active_viewers: item?.active_viewers || 1 });
});

// API Keys
app.get('/api/keys', (_req, res) => {
  res.json({ keys: Array.from(apiKeysMap.values()) });
});

app.post('/api/keys', (req, res) => {
  const name = String(req.body?.name || '').trim() || `API Key ${new Date().toISOString().slice(0, 16)}`;
  const rawHex = crypto.randomBytes(16).toString('hex');
  const key = `sm_live_${rawHex}`;
  const rec: APIKeyRecord = {
    key,
    prefix: `${key.slice(0, 12)}...`,
    name,
    created_at: new Date().toISOString(),
  };
  apiKeysMap.set(key, rec);
  res.status(201).json(rec);
});

app.delete(['/api/keys', '/api/keys/:key'], (req, res) => {
  const paramKey = Array.isArray(req.params.key) ? req.params.key[0] : req.params.key;
  const key = String(paramKey || req.query.key || req.body?.key || '').trim();
  apiKeysMap.delete(key);
  res.json({ status: 'deleted', key });
});

// File API v1
app.post('/api/v1/files-sync', (_req, res) => {
  res.json({ status: 'synced', purged: 0, remaining: filesMap.size });
});

app.post('/api/v1/files-upload', (req, res) => {
  const filename = String(req.body?.filename || 'Uploaded.Media.Asset.mp4');
  const sizeBytes = Number(req.body?.size_bytes || 512 * 1024 * 1024);
  const key = crypto.randomBytes(12).toString('base64url').slice(0, 16);
  const now = new Date().toISOString();
  const item: MediaItem = {
    key,
    state: 'completed',
    filename,
    size_bytes: sizeBytes,
    mime_type: 'video/mp4',
    created_by: 'Dashboard',
    progress_percent: 100,
    stage: 'ready',
    stage_name: 'Stored & Streamable',
    stage_percent: 100,
    transferred_bytes: sizeBytes,
    total_bytes: sizeBytes,
    source_type: 'upload',
    node_id: 'vps-hel-hdd-03',
    worker_node_id: 'vps-fra-nvme-01',
    placement: {
      tier_id: 2,
      tier_label: 'tier2',
      node_id: 'vps-hel-hdd-03',
      path: '/mnt/hdd0',
    },
    stream_url: `/stream/${key}.mp4`,
    play_url: `/stream/${key}`,
    score: 1.0,
    total_hits: 0,
    active_viewers: 0,
    last_hit_at: now,
    created_at: now,
    updated_at: now,
  };
  filesMap.set(key, item);
  res.status(201).json({
    key,
    state: item.state,
    filename,
    size_bytes: sizeBytes,
    mime_type: 'video/mp4',
    upload_url: `/api/v1/files/${key}/upload`,
    placement: item.placement,
    status_url: `/api/v1/files/${key}`,
    stream_url: item.stream_url,
    play_url: item.play_url,
    created_at: now,
  });
});

app.route('/api/v1/files')
  .get((_req, res) => {
    res.json({ files: Array.from(filesMap.values()) });
  })
  .post((req, res) => {
    const rawUrl = String(req.body?.url || '').trim();
    if (!rawUrl) {
      res.status(400).json({ error: "'url' is required" });
      return;
    }
    const key = crypto.randomBytes(12).toString('base64url').slice(0, 16);
    const filename = decodeURIComponent(rawUrl.split('/').pop()?.split('?')[0] || `stream_${key}.mp4`);
    const now = new Date().toISOString();
    const sizeBytes = 1024 * 1024 * 750;
    const item: MediaItem = {
      key,
      state: 'completed',
      filename,
      size_bytes: sizeBytes,
      mime_type: 'video/mp4',
      created_by: 'Dashboard',
      progress_percent: 100,
      stage: 'ready',
      stage_name: 'Stored & Streamable',
      stage_percent: 100,
      transferred_bytes: sizeBytes,
      total_bytes: sizeBytes,
      source_type: 'url',
      node_id: 'vps-hel-hdd-03',
      worker_node_id: 'vps-fra-nvme-01',
      placement: {
        tier_id: 2,
        tier_label: 'tier2',
        node_id: 'vps-hel-hdd-03',
        path: '/mnt/hdd0',
      },
      stream_url: `/stream/${key}.mp4`,
      play_url: `/stream/${key}`,
      score: 1.0,
      total_hits: 1,
      active_viewers: 0,
      last_hit_at: now,
      created_at: now,
      updated_at: now,
    };
    filesMap.set(key, item);
    res.status(201).json(item);
  })
  .delete((req, res) => {
    const keys: string[] = Array.isArray(req.body?.keys)
      ? req.body.keys
      : String(req.query.keys || '')
          .split(',')
          .filter(Boolean);
    const deleted: string[] = [];
    for (const k of keys) {
      if (filesMap.delete(k)) deleted.push(k);
    }
    res.json({ status: 'completed', deleted, failed: {}, count: deleted.length });
  });

app.all('/api/v1/files/:key*', (req, res) => {
  const rawAfter = req.path.replace(/^\/api\/v1\/files\//, '');
  const parts = rawAfter.split('/').filter(Boolean);
  const key = parts[0];
  const action = parts[1] || '';

  if (req.method === 'DELETE' && !action) {
    const keys = key.split(',').map((k) => k.trim()).filter(Boolean);
    const deleted: string[] = [];
    for (const k of keys) {
      if (filesMap.delete(k)) deleted.push(k);
    }
    res.json({ status: 'deleted', key, deleted, count: deleted.length });
    return;
  }

  const item = filesMap.get(key);
  if (!item) {
    res.status(404).json({ error: `file '${key}' not found` });
    return;
  }

  if (action === 'info' || action === 'status' || action === 'details') {
    res.json(item);
    return;
  }

  if (action === 'metadata' || action === 'meta') {
    res.json(item.metadata || { key, filename: item.filename, state: item.state, metadata: null });
    return;
  }

  if (action === 'placement') {
    res.json({
      placement: item.placement,
      state: {
        key: item.key,
        tier_id: item.placement.tier_id,
        score: item.score,
        total_hits: item.total_hits,
        pinned_tier: item.pinned_tier,
        pin_until: item.pin_until,
        last_hit_at: item.last_hit_at,
      },
      active_task: null,
      next: {
        action: 'hold',
        reason: 'Placed on optimal tier according to current heat policy',
      },
    });
    return;
  }

  if (action === 'move' && req.method === 'POST') {
    const targetTierId = Number(req.body?.tier_id ?? 0);
    const targetTier = tiersMap.get(targetTierId);
    const targetBlock = targetTier?.blocks[0];
    if (targetTier && targetBlock) {
      item.placement = {
        tier_id: targetTier.id,
        tier_label: `tier${targetTier.id}`,
        node_id: targetBlock.node_id,
        path: targetBlock.path,
      };
      item.updated_at = new Date().toISOString();
    }
    res.status(202).json({
      id: `lct_${crypto.randomBytes(6).toString('hex')}`,
      key: item.key,
      kind: 'manual',
      reason: `Manual relocation to Tier ${targetTierId}`,
      from: item.placement,
      to: item.placement,
      state: 'done',
      progress: 100,
      bytes: item.size_bytes,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    });
    return;
  }

  if (action === 'demand' && req.method === 'POST') {
    const { level, tier_id } = req.body || {};
    if (level === 'clear' || tier_id === null || tier_id === undefined) {
      item.pinned_tier = null;
      item.pin_until = null;
    } else {
      item.pinned_tier = Number(tier_id);
    }
    res.json({
      key: item.key,
      pinned_tier: item.pinned_tier,
      pin_until: item.pin_until,
      current_tier: item.placement.tier_id,
    });
    return;
  }

  if (action === 'upload') {
    item.state = 'completed';
    item.progress_percent = 100;
    res.status(202).json({ status: 'accepted', key, status_url: `/api/v1/files/${key}` });
    return;
  }

  const exp = Math.floor(Date.now() / 1000) + 4 * 3600;
  const ticket = `t_${crypto.randomBytes(12).toString('base64url')}`;
  res.json({
    key: item.key,
    ticket,
    filename: item.filename,
    token: crypto.randomBytes(16).toString('hex'),
    expires_at: exp,
    expires_in_sec: 14400,
    video_url: 'https://commondatastorage.googleapis.com/gtv-videos-bucket/sample/TearsOfSteel.mp4',
    play_url: `/stream/${ticket}`,
    audio_tracks: (item.metadata?.audio_tracks as unknown[]) || [],
    subtitles: (item.metadata?.subtitles as unknown[]) || [],
  });
});

// Database Studio (/api/dbadmin/*)
function buildDBTables(): Record<string, Map<string, unknown>> {
  const tables: Record<string, Map<string, unknown>> = {
    node: new Map(),
    config: new Map([['cluster_secret', { secret: '******', updated_at: new Date().toISOString() }]]),
    tier: new Map(),
    file: new Map(),
    fjob: new Map(),
    flow: new Map([['policy', flowPolicy]]),
    lcstate: new Map(),
    lctask: new Map(),
    lcactive: new Map(),
    lcaudit: new Map(),
    apikey: new Map(),
    user: new Map(),
    session: new Map(),
    bucket: new Map(),
  };

  for (const [k, v] of nodeConfigs.entries()) tables.node.set(k, v);
  for (const [k, v] of tiersMap.entries()) tables.tier.set(String(k), v);
  for (const [k, v] of filesMap.entries()) {
    tables.file.set(k, {
      key: v.key,
      filename: v.filename,
      size_bytes: v.size_bytes,
      mime_type: v.mime_type,
      source_type: v.source_type,
      created_by: v.created_by,
      created_at: v.created_at,
    });
    tables.fjob.set(k, v);
    tables.lcstate.set(k, {
      key: v.key,
      tier_id: v.placement.tier_id,
      score: v.score,
      total_hits: v.total_hits,
      last_hit_at: v.last_hit_at,
    });
  }
  for (const [k, v] of lifecycleTasks.entries()) tables.lctask.set(k, v);
  lifecycleAudits.forEach((a, idx) => tables.lcaudit.set(`${a.at}_${idx}`, a));
  for (const [k, v] of apiKeysMap.entries()) tables.apikey.set(k, v);
  for (const [k, v] of usersMap.entries()) tables.user.set(k, { username: v.username, created_at: v.created_at });
  for (const [k, v] of bucketsMap.entries()) tables.bucket.set(k, { ...v, secret_key: '******' });

  return tables;
}

app.all('/api/dbadmin/:action', (req, res) => {
  const action = req.params.action;
  const dbTables = buildDBTables();

  if (action === 'tables' && req.method === 'GET') {
    const tables = Object.entries(dbTables).map(([name, map]) => {
      let bytes = 0;
      for (const val of map.values()) {
        bytes += JSON.stringify(val).length;
      }
      return { name, count: map.size, bytes };
    });
    const storedBytes = tables.reduce((acc, t) => acc + t.bytes, 0);
    const keyCount = tables.reduce((acc, t) => acc + t.count, 0);
    res.json({
      tables,
      stats: {
        lsm_size: storedBytes * 2,
        stored_bytes: storedBytes,
        key_count: keyCount,
        table_count: tables.length,
      },
    });
    return;
  }

  if (action === 'rows' && req.method === 'GET') {
    const tableName = String(req.query.table || 'node');
    const search = String(req.query.search || '').toLowerCase().trim();
    const page = Math.max(1, Number(req.query.page) || 1);
    const limit = Math.max(1, Math.min(500, Number(req.query.limit) || 50));
    const map = dbTables[tableName] || new Map();

    const rows = Array.from(map.entries())
      .filter(([k, v]) => {
        if (!search) return true;
        return k.toLowerCase().includes(search) || JSON.stringify(v).toLowerCase().includes(search);
      })
      .map(([k, v]) => ({
        key: k,
        full_key: `${tableName}:${k}`,
        size: JSON.stringify(v).length,
        version: 1,
        value: v,
      }));

    res.json({
      rows: rows.slice((page - 1) * limit, page * limit),
      total: rows.length,
      page,
      limit,
      total_page: Math.max(1, Math.ceil(rows.length / limit)),
    });
    return;
  }

  if (action === 'entry' && req.method === 'GET') {
    const tableName = String(req.query.table || 'node');
    const key = String(req.query.key || '');
    const map = dbTables[tableName];
    const val = map?.get(key);
    if (!val) {
      res.status(404).json({ error: 'entry not found' });
      return;
    }
    res.json({
      key,
      full_key: `${tableName}:${key}`,
      size: JSON.stringify(val).length,
      version: 1,
      value: val,
    });
    return;
  }

  res.json({ status: 'ok' });
});

// OpenAPI & Embedded Swagger UI
app.get('/api/openapi.json', (_req, res) => {
  res.json({
    openapi: '3.0.0',
    info: {
      title: 'Stream Mesh // Video API (v1)',
      version: '1.0.0',
      description: 'Distributed video ingest, fast-start streaming, and storage lifecycle.',
    },
    servers: [{ url: '/', description: 'Cluster Coordinator' }],
    paths: {
      '/api/v1/files': {
        get: { summary: 'List all files in the cluster', responses: { '200': { description: 'File list' } } },
        post: { summary: 'Ingest a video by URL', responses: { '201': { description: 'Accepted' } } },
      },
      '/api/v1/files/{key}': {
        get: { summary: 'Player streaming URLs & anti-leech token', responses: { '200': { description: 'Stream URLs' } } },
        delete: { summary: 'Delete file from cluster', responses: { '200': { description: 'Deleted' } } },
      },
      '/api/v1/files/{key}/info': {
        get: { summary: 'Full file lifecycle, progress & placement info', responses: { '200': { description: 'File status' } } },
      },
    },
  });
});

app.get(['/docs/swagger-embed', '/docs/swagger-embed/'], (_req, res) => {
  res.setHeader('Content-Type', 'text/html; charset=utf-8');
  res.send(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>StreamMesh API Documentation</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui.css" />
  <style>body { margin: 0; background: #fbfaf8; } .topbar { display: none; }</style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui-bundle.js"></script>
  <script>
    window.onload = () => {
      window.ui = SwaggerUIBundle({ url: '/api/openapi.json', dom_id: '#swagger-ui', layout: 'BaseLayout' });
    };
  </script>
</body>
</html>`);
});

// HTML Dashboard Views
const validDashboardPaths: Record<string, string> = {
  '/': 'dashboard',
  '/dashboard': 'dashboard',
  '/nodes': 'nodes',
  '/storage': 'storage',
  '/tiers': 'tiers',
  '/flows': 'flows',
  '/flows-queue': 'flows-queue',
  '/flows-hot': 'flows-hot',
  '/flows-audit': 'flows-audit',
  '/media': 'media',
  '/database': 'database',
  '/settings': 'settings',
  '/apikeys': 'apikeys',
  '/docs': 'docs',
};

app.get('*', (req: Request, res: Response) => {
  const currentPage = validDashboardPaths[req.path];
  if (!currentPage) {
    res.status(404).send('Not Found');
    return;
  }
  const templates = loadTemplateMap();
  const html = renderGoTemplate('base.html', { CurrentPage: currentPage, NeedsAuth: false }, templates);
  res.setHeader('Content-Type', 'text/html; charset=utf-8');
  res.send(html);
});

app.listen(PORT, '0.0.0.0', () => {
  console.log(`Stream Coordinator ${CURRENT_VERSION} running at http://0.0.0.0:${PORT}`);
});
