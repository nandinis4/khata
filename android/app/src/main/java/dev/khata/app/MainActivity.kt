package dev.khata.app

import android.Manifest
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.core.content.ContextCompat
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.lifecycleScope
import dev.khata.app.ui.BudgetRow
import dev.khata.app.ui.KhataTheme
import dev.khata.app.ui.ReviewCard
import dev.khata.app.ui.SectionHeader
import dev.khata.app.ui.TransactionRow
import dev.khata.app.ui.prettyCategory
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.util.Calendar

data class HomeState(
    val loading: Boolean = true,
    val budgets: List<EnvelopeState> = emptyList(),
    val transactions: List<Transaction> = emptyList(),
    val review: List<Transaction> = emptyList(),
    val categories: List<String> = emptyList(),
    val ruleCount: Int = 0,
    val listenerEnabled: Boolean = false,
    val canNotify: Boolean = true,
)

class MainActivity : ComponentActivity() {

    private val state = MutableStateFlow(HomeState())

    // Hoisted rather than created in the composable: collectAsStateWithLifecycle
    // keys its effect on the flow instance, so a fresh one per recomposition
    // would tear down and restart collection every frame.
    private val stateFlow = state.asStateFlow()

    private val requestPermissions =
        registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {
            lifecycleScope.launch { refresh() }
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        askForPermissions()

        setContent {
            KhataTheme {
                val home by stateFlow.collectAsStateWithLifecycle()
                val scope = rememberCoroutineScope()

                HomeScreen(
                    state = home,
                    onEnableListener = { startActivity(Intent(LISTENER_SETTINGS)) },
                    onCategorise = { txn, category ->
                        scope.launch {
                            withContext(Dispatchers.IO) { Engine.correct(this@MainActivity, txn.id, category) }
                            refresh()
                        }
                    },
                    onSetBudget = { category, rupees ->
                        scope.launch {
                            withContext(Dispatchers.IO) {
                                Engine.setEnvelope(this@MainActivity, category, (rupees * 100).toLong())
                            }
                            refresh()
                        }
                    },
                )
            }
        }
    }

    override fun onResume() {
        super.onResume()
        // Refresh here rather than once at start: the user may have just
        // granted notification access, or filed something from the shade.
        lifecycleScope.launch { refresh() }
    }

    /**
     * Requests the two runtime permissions the app's features depend on.
     *
     * POST_NOTIFICATIONS is the important one — without it the review prompt,
     * which is the whole interaction model, silently never appears on Android 13
     * and later. SMS is genuinely optional and only asked for once; a user who
     * declines still gets everything the notification listener can see.
     */
    private fun askForPermissions() {
        val wanted = buildList {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
                !granted(Manifest.permission.POST_NOTIFICATIONS)
            ) {
                add(Manifest.permission.POST_NOTIFICATIONS)
            }
            if (!granted(Manifest.permission.RECEIVE_SMS)) {
                add(Manifest.permission.RECEIVE_SMS)
            }
        }
        if (wanted.isNotEmpty()) requestPermissions.launch(wanted.toTypedArray())
    }

    private fun granted(permission: String): Boolean =
        ContextCompat.checkSelfPermission(this, permission) == PackageManager.PERMISSION_GRANTED

    private suspend fun refresh() {
        val now = Calendar.getInstance()
        val year = now.get(Calendar.YEAR)
        val month = now.get(Calendar.MONTH) + 1

        val next = withContext(Dispatchers.IO) {
            HomeState(
                loading = false,
                budgets = Engine.monthStates(this@MainActivity, year, month),
                transactions = Engine.transactions(this@MainActivity, year, month),
                review = Engine.needsReview(this@MainActivity, limit = 20),
                categories = Engine.categories(this@MainActivity),
                ruleCount = Engine.ruleCount(this@MainActivity),
                listenerEnabled = isListenerEnabled(this@MainActivity),
                canNotify = Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU ||
                    granted(Manifest.permission.POST_NOTIFICATIONS),
            )
        }
        state.value = next
    }

    companion object {
        private const val LISTENER_SETTINGS = Settings.ACTION_NOTIFICATION_LISTENER_SETTINGS

        /**
         * Notification access cannot be requested with a runtime permission
         * dialog; the user has to grant it in system settings, and the only way
         * to know whether they did is to read the enabled-listeners setting.
         */
        fun isListenerEnabled(context: Context): Boolean {
            val enabled = Settings.Secure.getString(
                context.contentResolver,
                "enabled_notification_listeners",
            ).orEmpty()
            return enabled.contains(context.packageName)
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun HomeScreen(
    state: HomeState,
    onEnableListener: () -> Unit,
    onCategorise: (Transaction, String) -> Unit,
    onSetBudget: (String, Double) -> Unit,
) {
    var budgetTarget by remember { mutableStateOf<String?>(null) }

    Scaffold(
        topBar = { TopAppBar(title = { Text("khata") }) }
    ) { padding ->
        if (state.loading) {
            Box(Modifier.fillMaxSize().padding(padding), contentAlignment = Alignment.Center) {
                CircularProgressIndicator()
            }
            return@Scaffold
        }

        LazyColumn(
            modifier = Modifier.fillMaxSize().padding(padding),
            contentPadding = PaddingValues(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            if (!state.listenerEnabled) {
                item(key = "enable-capture") { EnableCaptureCard(onEnableListener) }
            }
            if (!state.canNotify) {
                item(key = "enable-notifications") { NotificationsOffCard() }
            }

            if (state.review.isNotEmpty()) {
                item(key = "hdr-review") { SectionHeader("Needs a decision", "${state.review.size}") }
                // Keys are namespaced because a transaction can legitimately
                // appear both here and under Recent, and LazyColumn keys must be
                // unique across the whole list or it throws.
                items(state.review, key = { "review:${it.id}" }) { txn ->
                    ReviewCard(
                        txn = txn,
                        categories = state.categories,
                        onPick = { category -> onCategorise(txn, category) },
                    )
                }
            }

            val budgeted = state.budgets.filter { it.hasBudget }
            if (budgeted.isNotEmpty()) {
                item(key = "hdr-budgets") { SectionHeader("This month") }
                items(budgeted, key = { "budget:${it.category}" }) { envelope ->
                    BudgetRow(envelope, onClick = { budgetTarget = envelope.category })
                }
            }

            val unbudgeted = state.budgets.filterNot { it.hasBudget }.filter { it.spentMinor > 0 }
            if (unbudgeted.isNotEmpty()) {
                item(key = "hdr-unbudgeted") { SectionHeader("No budget set") }
                items(unbudgeted, key = { "unbudgeted:${it.category}" }) { envelope ->
                    BudgetRow(envelope, onClick = { budgetTarget = envelope.category })
                }
            }

            if (state.transactions.isNotEmpty()) {
                item(key = "hdr-recent") { SectionHeader("Recent") }
                items(state.transactions.take(40), key = { "txn:${it.id}" }) { TransactionRow(it) }
            }

            item(key = "footer") { PrivacyFooter(state.ruleCount) }
        }
    }

    budgetTarget?.let { category ->
        SetBudgetDialog(
            category = category,
            onDismiss = { budgetTarget = null },
            onConfirm = { rupees ->
                onSetBudget(category, rupees)
                budgetTarget = null
            },
        )
    }
}

@Composable
private fun SetBudgetDialog(category: String, onDismiss: () -> Unit, onConfirm: (Double) -> Unit) {
    var text by remember { mutableStateOf("") }
    val amount = text.toDoubleOrNull()

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Monthly budget for ${prettyCategory(category)}") },
        text = {
            OutlinedTextField(
                value = text,
                onValueChange = { text = it.filter { c -> c.isDigit() || c == '.' } },
                label = { Text("Amount in ₹") },
                singleLine = true,
                keyboardOptions = androidx.compose.foundation.text.KeyboardOptions(
                    keyboardType = KeyboardType.Decimal,
                ),
            )
        },
        confirmButton = {
            TextButton(onClick = { amount?.let(onConfirm) }, enabled = amount != null && amount > 0) {
                Text("Set")
            }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

@Composable
private fun EnableCaptureCard(onEnable: () -> Unit) {
    Card {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("Turn on notification access", style = MaterialTheme.typography.titleMedium)
            Text(
                "khata reads transaction alerts from your bank and payment apps to build your " +
                    "ledger. It only looks at notifications from apps on its allowlist, and it " +
                    "has no internet permission, so nothing it reads can leave this phone.",
                style = MaterialTheme.typography.bodyMedium,
            )
            Button(onClick = onEnable) { Text("Open settings") }
        }
    }
}

@Composable
private fun NotificationsOffCard() {
    Card {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("Notifications are off", style = MaterialTheme.typography.titleMedium)
            Text(
                "khata will still record your transactions, but it cannot ask you about the ones " +
                    "it is unsure of, and it cannot tell you when a budget is running out. " +
                    "You can turn notifications on in Android settings.",
                style = MaterialTheme.typography.bodyMedium,
            )
        }
    }
}

@Composable
private fun PrivacyFooter(ruleCount: Int) {
    Column(Modifier.padding(vertical = 24.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
        Text(
            "$ruleCount categorisation rules, all running on this device.",
            style = MaterialTheme.typography.bodySmall,
        )
        Text(
            "khata has no internet permission. Your transactions are stored in one file " +
                "that only this app can read, and they are never uploaded, synced or shared.",
            style = MaterialTheme.typography.bodySmall,
            fontWeight = FontWeight.Medium,
        )
        Text("engine ${Engine.version()}", style = MaterialTheme.typography.labelSmall)
    }
}
