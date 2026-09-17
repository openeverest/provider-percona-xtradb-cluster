// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	// A pxcConfigSizeSmall is the configuration for PXC cluster with the dimension of 1 vCPU and 2GB RAM.
	//nolint:lll
	pxcConfigSizeSmall = `[mysqld]
binlog_cache_size = 131072
binlog_expire_logs_seconds = 604800
binlog_format = ROW
binlog_stmt_cache_size = 131072
global-connection-memory-limit = 18446744073709551615
global-connection-memory-tracking = false
innodb_adaptive_hash_index = True
innodb_buffer_pool_chunk_size = 2097152
innodb_buffer_pool_instances = 1
innodb_buffer_pool_size = 1398838681
innodb_ddl_threads = 2
innodb_flush_log_at_trx_commit = 2
innodb_flush_method = O_DIRECT
innodb_io_capacity_max = 1800
innodb_monitor_enable = ALL
innodb_page_cleaners = 1
innodb_parallel_read_threads = 1
innodb_purge_threads = 4
innodb_redo_log_capacity = 415269664
join_buffer_size = 524288
max_connections = 152
max_heap_table_size = 16777216
read_rnd_buffer_size = 393216
replica_compressed_protocol = 1
replica_exec_mode = STRICT
replica_parallel_type = LOGICAL_CLOCK
replica_parallel_workers = 4
replica_preserve_commit_order = ON
sort_buffer_size = 524288
sql_mode = 'ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION,TRADITIONAL,STRICT_ALL_TABLES'
sync_binlog = 1
table_definition_cache = 4096
table_open_cache = 4096
table_open_cache_instances = 4
tablespace_definition_cache = 512
thread_cache_size = 11
thread_pool_size = 4
thread_stack = 1048576
tmp_table_size = 16777216
wsrep_slave_threads = 1
wsrep_sync_wait = 3
wsrep_trx_fragment_size = 1048576
wsrep_trx_fragment_unit = bytes
wsrep-provider-options = evs.delayed_keep_period=PT545S;evs.inactive_timeout=PT90S;gmcast.peer_timeout=PT11S;gmcast.time_wait=PT13S;pc.linger=PT45S;evs.delay_margin=PT22S;evs.suspect_timeout=PT45S;gcs.fc_limit=96;gcs.max_packet_size=98304;evs.send_window=768;evs.user_send_window=768;evs.join_retrans_period=PT3S;evs.inactive_check_period=PT3S;evs.stats_report_period=PT1M;evs.max_install_timeouts=3;pc.announce_timeout=PT45S;pc.recovery=true;gcache.size=477560113;gcache.recover=yes;
    `
	// A pxcConfigSizeMedium is the configuration for PXC cluster with the dimension of 4 vCPU and 8GB RAM.
	//nolint:lll
	pxcConfigSizeMedium = `[mysqld]
binlog_cache_size = 131072
binlog_expire_logs_seconds = 604800
binlog_format = ROW
binlog_stmt_cache_size = 131072
global-connection-memory-limit = 18446744073709551615
global-connection-memory-tracking = false
innodb_adaptive_hash_index = True
innodb_buffer_pool_chunk_size = 2097152
innodb_buffer_pool_instances = 6
innodb_buffer_pool_size = 5512928528
innodb_flush_log_at_trx_commit = 2
innodb_flush_method = O_DIRECT
innodb_io_capacity_max = 1800
innodb_monitor_enable = ALL
innodb_page_cleaners = 6
innodb_parallel_read_threads = 3
innodb_purge_threads = 4
innodb_redo_log_capacity = 1710392192
join_buffer_size = 524288
max_connections = 802
max_heap_table_size = 16777216
read_rnd_buffer_size = 393216
replica_compressed_protocol = 1
replica_exec_mode = STRICT
replica_parallel_type = LOGICAL_CLOCK
replica_parallel_workers = 4
replica_preserve_commit_order = ON
sort_buffer_size = 524288
sql_mode = 'ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION,TRADITIONAL,STRICT_ALL_TABLES'
sync_binlog = 1
table_definition_cache = 4096
table_open_cache = 4096
table_open_cache_instances = 4
tablespace_definition_cache = 512
thread_cache_size = 24
thread_pool_size = 6
thread_stack = 1048576
tmp_table_size = 16777216
wsrep_slave_threads = 1
wsrep_sync_wait = 3
wsrep_trx_fragment_size = 1048576
wsrep_trx_fragment_unit = bytes
wsrep-provider-options = evs.suspect_timeout=PT45S;gcs.max_packet_size=98365;evs.delay_margin=PT22S;evs.inactive_timeout=PT90S;evs.join_retrans_period=PT3S;gmcast.peer_timeout=PT11S;gcache.size=1966951020;evs.send_window=768;evs.user_send_window=768;evs.max_install_timeouts=3;pc.linger=PT45S;evs.stats_report_period=PT1M;gcs.fc_limit=96;gmcast.time_wait=PT13S;evs.inactive_check_period=PT3S;pc.announce_timeout=PT45S;pc.recovery=true;gcache.recover=yes;evs.delayed_keep_period=PT545S;
	`
	// A pxcConfigSizeLarge is the configuration for PXC cluster with the dimension of 8 vCPU and 32GB RAM.
	//nolint:lll
	pxcConfigSizeLarge = `[mysqld]
binlog_cache_size = 131072
binlog_expire_logs_seconds = 604800
binlog_format = ROW
binlog_stmt_cache_size = 131072
global-connection-memory-limit = 18446744073709551615
global-connection-memory-tracking = false
innodb_adaptive_hash_index = True
innodb_buffer_pool_chunk_size = 2097152
innodb_buffer_pool_instances = 6
innodb_buffer_pool_size = 23154566817
innodb_flush_log_at_trx_commit = 2
innodb_flush_method = O_DIRECT
innodb_io_capacity_max = 1800
innodb_monitor_enable = ALL
innodb_page_cleaners = 6
innodb_parallel_read_threads = 3
innodb_purge_threads = 4
innodb_redo_log_capacity = 7816840704
join_buffer_size = 524288
max_connections = 1602
max_heap_table_size = 16777216
read_rnd_buffer_size = 393216
replica_compressed_protocol = 1
replica_exec_mode = STRICT
replica_parallel_type = LOGICAL_CLOCK
replica_parallel_workers = 4
replica_preserve_commit_order = ON
sort_buffer_size = 524288
sql_mode = 'ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION,TRADITIONAL,STRICT_ALL_TABLES'
sync_binlog = 1
table_definition_cache = 4096
table_open_cache = 4096
table_open_cache_instances = 4
tablespace_definition_cache = 512
thread_cache_size = 40
thread_pool_size = 6
thread_stack = 1048576
tmp_table_size = 16777216
wsrep_slave_threads = 1
wsrep_sync_wait = 3
wsrep_trx_fragment_size = 1048576
wsrep_trx_fragment_unit = bytes
wsrep-provider-options = evs.delayed_keep_period=PT560S;evs.stats_report_period=PT1M;gcs.fc_limit=128;gmcast.peer_timeout=PT15S;gmcast.time_wait=PT18S;evs.max_install_timeouts=5;pc.recovery=true;gcache.recover=yes;gcache.size=8989366809;evs.delay_margin=PT30S;evs.user_send_window=1024;evs.inactive_check_period=PT5S;evs.join_retrans_period=PT5S;evs.suspect_timeout=PT60S;gcs.max_packet_size=131072;pc.linger=PT60S;evs.send_window=1024;evs.inactive_timeout=PT120S;pc.announce_timeout=PT60S;
	`
	// pxcConfigSizeTiny is for pods smaller than 2Gi (e.g. CI). Image defaults
	// still use a 128M gcache and can OOM or fill a small PVC.
	pxcConfigSizeTiny = `[mysqld]
binlog_format = ROW
innodb_buffer_pool_size = 128M
innodb_buffer_pool_instances = 1
innodb_buffer_pool_chunk_size = 2097152
innodb_redo_log_capacity = 32M
max_connections = 50
wsrep-provider-options = gcache.size=32M;gcache.recover=yes;
`
)

var (
	pxcMemSmall  = resource.MustParse("2Gi")
	pxcMemMedium = resource.MustParse("8Gi")
	pxcMemLarge  = resource.MustParse("32Gi")
)

func memoryLimitOrRequest(resources *corev1.ResourceRequirements) resource.Quantity {
	if resources == nil {
		return resource.Quantity{}
	}
	if q, ok := resources.Limits[corev1.ResourceMemory]; ok && !q.IsZero() {
		return q
	}
	if q, ok := resources.Requests[corev1.ResourceMemory]; ok {
		return q
	}
	return resource.Quantity{}
}

// defaultConfigurationForEngine picks a my.cnf preset from replica count and
// engine memory. Small/medium/large assume 2Gi / 8Gi / 32Gi; anything smaller
// gets tiny so the buffer pool fits the pod.
func defaultConfigurationForEngine(replicas int32, resources *corev1.ResourceRequirements) string {
	mem := memoryLimitOrRequest(resources)
	switch {
	case replicas != 1 && replicas != 3 && mem.Cmp(pxcMemLarge) >= 0:
		return pxcConfigSizeLarge
	case replicas == 3 && mem.Cmp(pxcMemMedium) >= 0:
		return pxcConfigSizeMedium
	case mem.Cmp(pxcMemSmall) >= 0:
		return pxcConfigSizeSmall
	default:
		return pxcConfigSizeTiny
	}
}
