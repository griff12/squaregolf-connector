package com.squaregolf.connector

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.collectAsState
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.lifecycle.viewmodel.compose.viewModel

/**
 * One screen, one gate.
 *
 * The Activity holds no connector state. Everything lives in [ConnectorViewModel], which
 * survives configuration change, and in [Native], which is process-scoped. The manifest
 * also declares configChanges for the fold-relevant qualifiers, so on a Z Fold the common
 * case never recreates this Activity at all.
 */
class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        setContent {
            MaterialTheme {
                Surface(modifier = Modifier.fillMaxSize()) {
                    GateScreen()
                }
            }
        }
    }
}

@Composable
private fun GateScreen(vm: ConnectorViewModel = viewModel()) {
    val s by vm.state.collectAsState()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .safeDrawingPadding()
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Text("SquareGolf Connector - Phase 1", style = MaterialTheme.typography.titleLarge)
        Text(
            "core " + s.nativeVersion,
            style = MaterialTheme.typography.bodySmall,
            fontFamily = FontFamily.Monospace,
        )

        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            OutlinedTextField(
                value = s.host,
                onValueChange = vm::setHost,
                label = { Text("GSPro host") },
                singleLine = true,
                enabled = !s.busy,
                modifier = Modifier.weight(2f),
            )
            OutlinedTextField(
                value = s.port,
                onValueChange = vm::setPort,
                label = { Text("Port") },
                singleLine = true,
                enabled = !s.busy,
                modifier = Modifier.weight(1f),
            )
        }

        Card(modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                Text("launch monitor : " + s.lmStatus, fontFamily = FontFamily.Monospace)
                Text("gspro          : " + s.gsproStatus, fontFamily = FontFamily.Monospace)
                Text("armed          : " + s.armed, fontFamily = FontFamily.Monospace)
                Text("phase          : " + s.phase, fontFamily = FontFamily.Monospace)
                if (s.endpointInUse.isNotEmpty()) {
                    Text("endpoint       : " + s.endpointInUse, fontFamily = FontFamily.Monospace)
                }
                if (s.dropped > 0L) {
                    Text("dropped        : " + s.dropped, fontFamily = FontFamily.Monospace)
                }
            }
        }

        Row(
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Button(onClick = vm::fire, enabled = !s.busy) {
                Text(if (s.busy) "Working…" else "Connect & fire synthetic shot")
            }
            OutlinedButton(onClick = vm::disarm, enabled = s.armed) { Text("Disarm") }
        }

        if (s.busy || s.armed) {
            Text(
                "t+" + s.elapsedSec + "s   " +
                    "(launch monitor ~5 s, GSPro ~0.5 s, then a shot every ~11.5 s; " +
                    "first ball about 8.5 s after arming)",
                style = MaterialTheme.typography.bodySmall,
            )
        }

        if (s.lastShot.isNotEmpty()) {
            Card(modifier = Modifier.fillMaxWidth()) {
                Column(Modifier.padding(12.dp)) {
                    Text("last shot", fontWeight = FontWeight.Bold)
                    Text(s.lastShot, fontFamily = FontFamily.Monospace)
                }
            }
        }

        if (s.error.isNotEmpty()) {
            Card(modifier = Modifier.fillMaxWidth()) {
                Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text("error", color = MaterialTheme.colorScheme.error, fontWeight = FontWeight.Bold)
                    Text(s.error, color = MaterialTheme.colorScheme.error)
                    OutlinedButton(onClick = vm::clearError) { Text("Dismiss") }
                }
            }
        }

        Spacer(Modifier.height(2.dp))
        Text("engine log", style = MaterialTheme.typography.labelLarge)
        LazyColumn(modifier = Modifier.fillMaxWidth().weight(1f, fill = true)) {
            items(s.lines.asReversed()) { line ->
                Text(line, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace)
            }
        }
    }
}

