<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { api } from '../api'
type AuditKind = 'migration' | 'shard_ddl'
type AuditStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled' | 'interrupted'
interface AuditEvent { id:number; task_id:string; kind:AuditKind; data_source_id:string; tenant_id?:string; shard_rule_id?:string; action:string; from_status?:string; to_status:AuditStatus; actor:string; request_source?:string; message?:string; source_table?:string; target_table?:string; occurred_at:string }
interface AuditResponse { items:AuditEvent[]; total:number; limit:number; offset:number }
const events=ref<AuditEvent[]>([]), loading=ref(false), kind=ref(''), status=ref(''), page=ref(1), total=ref(0)
const pageSize=20
const statuses:AuditStatus[]=['queued','running','succeeded','failed','canceled','interrupted']
function eventKey(item:AuditEvent){return item.kind+'-'+item.id}
function statusLabel(value:AuditStatus){const labels:Record<string,string>={queued:'排队中',running:'执行中',succeeded:'成功',failed:'失败',canceled:'已取消',interrupted:'中断'};return labels[value]??value}
function statusType(value:AuditStatus){if(value==='succeeded')return 'success';if(value==='failed'||value==='interrupted')return 'danger';if(value==='running')return 'primary';if(value==='canceled')return 'info';return 'warning'}
function kindLabel(value:AuditKind){return value==='migration'?'数据库迁移':'分片 DDL'}
function targetLabel(item:AuditEvent){if(item.kind==='shard_ddl')return (item.source_table||'—')+' → '+(item.target_table||'—');return item.tenant_id?'租户 '+item.tenant_id:'数据源 '+item.data_source_id}
function actionLabel(item:AuditEvent){if(item.kind==='shard_ddl')return item.action==='clone'?'物理表结构克隆':item.action;const labels:Record<string,string>={up:'升级迁移',down:'回滚一步',down_steps:'按步数回滚',to_version:'迁移到指定版本'};return labels[item.action]??item.action}
async function loadEvents(){loading.value=true;try{const params=new URLSearchParams({limit:String(pageSize),offset:String((page.value-1)*pageSize)});if(kind.value)params.set('kind',kind.value);if(status.value)params.set('status',status.value);const result=await api.get<AuditResponse>('/audit-events?'+params.toString());events.value=result.data.items;total.value=result.data.total}catch{ElMessage.error('操作审计加载失败')}finally{loading.value=false}}
function applyFilters(){page.value=1;void loadEvents()}
function changePage(next:number){page.value=next;void loadEvents()}
onMounted(()=>void loadEvents())
</script>
<template>
<div class="page-heading"><div><div class="eyebrow">OPERATIONS / AUDIT TRAIL</div><h1>操作审计</h1><p>统一检索数据库迁移与分片 DDL 的状态变更记录，按时间倒序查看操作轨迹。</p></div><el-button :loading="loading" @click="loadEvents">刷新记录</el-button></div>
<el-alert title="身份与来源说明" description="当前 API 尚未接入登录认证。操作者 local-api 是固定服务标记，不代表真实登录用户；请求来源是管理 API 连接来源，可能是本机代理地址。" type="info" :closable="false" show-icon class="audit-note"/>
<el-card shadow="never" class="audit-card"><div class="audit-toolbar"><strong>审计事件</strong><div class="audit-filters">
<el-select v-model="kind" clearable placeholder="全部操作类型" style="width:160px" @change="applyFilters"><el-option label="数据库迁移" value="migration"/><el-option label="分片 DDL" value="shard_ddl"/></el-select>
<el-select v-model="status" clearable placeholder="全部状态" style="width:140px" @change="applyFilters"><el-option v-for="item in statuses" :key="item" :label="statusLabel(item)" :value="item"/></el-select>
</div></div>
<el-table :data="events" v-loading="loading" :row-key="eventKey" empty-text="暂无审计事件">
<el-table-column label="时间" min-width="180"><template #default="{row}">{{new Date(row.occurred_at).toLocaleString()}}</template></el-table-column>
<el-table-column label="类型" width="120"><template #default="{row}">{{kindLabel(row.kind)}}</template></el-table-column>
<el-table-column label="动作" width="145"><template #default="{row}">{{actionLabel(row)}}</template></el-table-column>
<el-table-column label="目标对象" min-width="220" show-overflow-tooltip><template #default="{row}">{{targetLabel(row)}}</template></el-table-column>
<el-table-column label="状态变化" min-width="150"><template #default="{row}"><span>{{row.from_status||'—'}} → </span><el-tag :type="statusType(row.to_status)" effect="plain">{{statusLabel(row.to_status)}}</el-tag></template></el-table-column>
<el-table-column prop="actor" label="操作者" width="120"/><el-table-column prop="request_source" label="请求来源" min-width="140" show-overflow-tooltip/><el-table-column prop="message" label="事件说明" min-width="230" show-overflow-tooltip/>
<el-table-column label="任务 ID" min-width="190" show-overflow-tooltip><template #default="{row}">{{row.task_id}}</template></el-table-column>
</el-table><div class="audit-pagination"><span>共 {{total}} 条记录</span><el-pagination :current-page="page" :page-size="pageSize" :total="total" layout="prev, pager, next" @current-change="changePage"/></div></el-card>
</template>
<style scoped>
.audit-note{margin-bottom:16px}.audit-card{border:1px solid #e8edf4;border-radius:12px}.audit-toolbar,.audit-pagination{display:flex;justify-content:space-between;align-items:center;gap:14px;margin-bottom:16px}.audit-toolbar>strong{color:#26354d;font-size:14px}.audit-filters{display:flex;gap:9px}.audit-pagination{justify-content:flex-end;margin:18px 0 0;color:#78859a;font-size:12px}@media(max-width:760px){.audit-toolbar{align-items:flex-start;flex-direction:column}.audit-filters{width:100%}.audit-filters .el-select{flex:1;min-width:0}.audit-pagination{align-items:flex-start;flex-direction:column}}
</style>