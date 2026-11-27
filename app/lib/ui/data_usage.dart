import 'dart:async';

import 'package:material_ui/material_ui.dart';

import '../core/core_api.dart';
import '../core/core_controller.dart';
import '../core/models.dart';
import '../l10n/app_localizations.dart';
import 'format.dart';
import 'widgets.dart';

/// The span the total covers.
enum _Period { all, week, month }

/// The data used: a total (everything, or the last 7 or 30 days) in large
/// figures, then a table of the days, newest first, [daysPerPage] to a
/// page: every day since the last reset. A day's row is download and upload
/// together; a tap shows its VPN and direct parts. Today's figures follow
/// while it is shown.
class DataUsageView extends StatefulWidget {
  const DataUsageView({super.key, required this.core});

  static const daysPerPage = 10;

  final CoreController core;

  @override
  State<DataUsageView> createState() => _DataUsageViewState();
}

class _DataUsageViewState extends State<DataUsageView> {
  static const _refresh = Duration(seconds: 5);

  DataUsage? _usage;
  CoreUnavailable? _problem;
  Timer? _timer;
  _Period _period = _Period.all;

  /// The page of days shown, 0 the newest.
  int _page = 0;

  @override
  void initState() {
    super.initState();
    _load();
    _timer = Timer.periodic(_refresh, (_) => _load());
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }

  Future<void> _load() async {
    try {
      final usage = await widget.core.call((api) => api.usage());
      if (mounted) {
        setState(() {
          _usage = usage;
          _problem = null;
        });
      }
    } on CoreUnavailable catch (e) {
      if (mounted) setState(() => _problem = e);
    }
  }

  /// The total of the chosen span: every day, or the last ones.
  UsageCounts _total(DataUsage usage) {
    if (_period == _Period.all) {
      return UsageCounts.sum(usage.days.map((d) => d.counts));
    }
    final now = DateTime.now();
    final first = DateTime(
      now.year,
      now.month,
      now.day,
    ).subtract(Duration(days: _period == _Period.week ? 6 : 29));
    return UsageCounts.sum([
      for (final d in usage.days)
        if (d.date != null && !d.date!.isBefore(first)) d.counts,
    ]);
  }

  /// Asks, then forgets every day; counting starts from zero.
  Future<void> _reset() async {
    final l = AppLocalizations.of(context);
    final yes = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text(l.usageResetTitle),
        content: SizedBox(width: 380, child: Text(l.usageResetBody)),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: Text(l.cancel),
          ),
          FilledButton(
            style: FilledButton.styleFrom(backgroundColor: StatusColors.bad),
            onPressed: () => Navigator.pop(context, true),
            child: Text(l.usageResetConfirm),
          ),
        ],
      ),
    );
    if (yes != true || !mounted) return;
    await runShowingErrors(context, () async {
      final usage = await widget.core.call((api) => api.resetUsage());
      if (mounted) {
        setState(() {
          _usage = usage;
          _page = 0;
        });
      }
    });
  }

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final usage = _usage;
    final problem = _problem;
    if (usage == null && problem != null) {
      return Panel(child: Text(describeProblem(l, problem)));
    }
    if (usage == null) {
      return const Padding(
        padding: EdgeInsets.all(24),
        child: Center(child: CircularProgressIndicator()),
      );
    }
    if (usage.days.isEmpty) {
      return Panel(child: Text(l.usageNone, style: muted));
    }
    const per = DataUsageView.daysPerPage;
    final pages = (usage.days.length + per - 1) ~/ per;
    final page = pages == 0 ? 0 : _page.clamp(0, pages - 1);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _TotalCard(
          counts: _total(usage),
          period: _period,
          onPeriod: (p) => setState(() => _period = p),
        ),
        const SizedBox(height: 16),
        Card(
          margin: EdgeInsets.zero,
          clipBehavior: Clip.antiAlias,
          child: LayoutBuilder(
            builder: (context, box) {
              final columns = _Columns.of(box.maxWidth);
              return Column(
                children: [
                  _DaysHeader(columns: columns),
                  for (final day in usage.days.skip(page * per).take(per)) ...[
                    const Divider(height: 1),
                    _DayRow(
                      key: ValueKey(day.date),
                      day: day,
                      columns: columns,
                    ),
                  ],
                ],
              );
            },
          ),
        ),
        if (pages > 1)
          Padding(
            padding: const EdgeInsets.only(top: 8),
            child: Row(
              mainAxisAlignment: MainAxisAlignment.center,
              children: [
                IconButton(
                  tooltip: l.usageNewer,
                  icon: const Icon(Icons.chevron_left_rounded),
                  onPressed: page > 0
                      ? () => setState(() => _page = page - 1)
                      : null,
                ),
                const SizedBox(width: 8),
                Text(l.usagePage(page + 1, pages), style: muted),
                const SizedBox(width: 8),
                IconButton(
                  tooltip: l.usageOlder,
                  icon: const Icon(Icons.chevron_right_rounded),
                  onPressed: page < pages - 1
                      ? () => setState(() => _page = page + 1)
                      : null,
                ),
              ],
            ),
          ),
        const SizedBox(height: 12),
        Align(
          alignment: AlignmentDirectional.centerEnd,
          // Red and outlined: it deletes every day (after a confirmation).
          child: OutlinedButton.icon(
            style: OutlinedButton.styleFrom(
              foregroundColor: StatusColors.bad,
              side: BorderSide(color: StatusColors.bad.withValues(alpha: 0.6)),
            ),
            onPressed: _reset,
            icon: const Icon(Icons.restart_alt_rounded, size: 18),
            label: Text(l.usageReset),
          ),
        ),
      ],
    );
  }
}

/// The total: the span to cover, and download and upload in large figures
/// (through the VPN and directly together; a day's row tells them apart).
class _TotalCard extends StatelessWidget {
  const _TotalCard({
    required this.counts,
    required this.period,
    required this.onPeriod,
  });

  final UsageCounts counts;
  final _Period period;
  final ValueChanged<_Period> onPeriod;

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    final all = counts.all;
    return Card(
      margin: EdgeInsets.zero,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(20, 16, 20, 18),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Wrap(
              alignment: WrapAlignment.spaceBetween,
              crossAxisAlignment: WrapCrossAlignment.center,
              spacing: 12,
              runSpacing: 10,
              children: [
                Text(
                  l.usageTotal,
                  style: theme.textTheme.titleMedium?.copyWith(
                    fontWeight: FontWeight.w700,
                  ),
                ),
                SegmentedButton<_Period>(
                  showSelectedIcon: false,
                  style: const ButtonStyle(
                    visualDensity: VisualDensity.compact,
                    tapTargetSize: MaterialTapTargetSize.shrinkWrap,
                  ),
                  segments: [
                    ButtonSegment(value: _Period.all, label: Text(l.usageAll)),
                    ButtonSegment(
                      value: _Period.week,
                      label: Text(l.usageWeek),
                    ),
                    ButtonSegment(
                      value: _Period.month,
                      label: Text(l.usageMonth),
                    ),
                  ],
                  selected: {period},
                  onSelectionChanged: (v) => onPeriod(v.first),
                ),
              ],
            ),
            const SizedBox(height: 18),
            Row(
              children: [
                Expanded(
                  child: _BigFigure(
                    arrow: Icons.arrow_downward_rounded,
                    cloud: Icons.cloud_download_rounded,
                    color: downloadColor,
                    name: l.usageDown,
                    bytes: all.down,
                  ),
                ),
                const SizedBox(width: 12),
                Expanded(
                  child: _BigFigure(
                    arrow: Icons.arrow_upward_rounded,
                    cloud: Icons.cloud_upload_rounded,
                    color: uploadColor,
                    name: l.usageUp,
                    bytes: all.up,
                  ),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}

/// A large figure under its name, in a box of its color: download or
/// upload. A box with room shows a cloud with the arrow too.
class _BigFigure extends StatelessWidget {
  const _BigFigure({
    required this.arrow,
    required this.cloud,
    required this.color,
    required this.name,
    required this.bytes,
  });

  final IconData arrow;
  final IconData cloud;
  final Color color;
  final String name;
  final int bytes;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final dark = theme.brightness == Brightness.dark;
    return LayoutBuilder(
      builder: (context, box) => Container(
        padding: const EdgeInsetsDirectional.fromSTEB(14, 12, 12, 14),
        decoration: BoxDecoration(
          color: color.withValues(alpha: dark ? 0.14 : 0.08),
          borderRadius: BorderRadius.circular(16),
          border: Border.all(
            color: color.withValues(alpha: dark ? 0.32 : 0.22),
          ),
        ),
        child: Row(
          children: [
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(
                    children: [
                      Icon(arrow, size: 16, color: color),
                      const SizedBox(width: 4),
                      Flexible(
                        child: Text(
                          name,
                          overflow: TextOverflow.ellipsis,
                          style: theme.textTheme.labelLarge?.copyWith(
                            color: theme.colorScheme.onSurfaceVariant,
                          ),
                        ),
                      ),
                    ],
                  ),
                  const SizedBox(height: 6),
                  FittedBox(
                    fit: BoxFit.scaleDown,
                    alignment: AlignmentDirectional.centerStart,
                    child: Text(
                      formatBytes(bytes),
                      style: theme.textTheme.headlineSmall?.copyWith(
                        fontWeight: FontWeight.w700,
                        fontFeatures: const [FontFeature.tabularFigures()],
                      ),
                    ),
                  ),
                ],
              ),
            ),
            if (box.maxWidth >= 190) ...[
              const SizedBox(width: 8),
              Container(
                width: 44,
                height: 44,
                decoration: BoxDecoration(
                  color: color.withValues(alpha: dark ? 0.24 : 0.14),
                  shape: BoxShape.circle,
                ),
                child: Icon(cloud, size: 24, color: color),
              ),
            ],
          ],
        ),
      ),
    );
  }
}

/// The widths of the table's columns of figures, narrower on a phone.
class _Columns {
  const _Columns(this.figure, this.total);

  /// Download and upload.
  final double figure;

  /// The day's total.
  final double total;

  static _Columns of(double width) =>
      width < 440 ? const _Columns(66, 84) : const _Columns(88, 100);
}

/// The room the expand arrow takes at the end of a row.
const double _arrowWidth = 28;

/// The table's column names: download, upload, and the day's total.
class _DaysHeader extends StatelessWidget {
  const _DaysHeader({required this.columns});

  final _Columns columns;

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    final style = theme.textTheme.labelSmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
      fontWeight: FontWeight.w600,
    );
    Widget name(IconData arrow, Color color, String text, [double? width]) =>
        SizedBox(
          width: width ?? columns.figure,
          child: Row(
            mainAxisAlignment: MainAxisAlignment.end,
            children: [
              Icon(arrow, size: 13, color: color),
              const SizedBox(width: 3),
              Flexible(
                child: Text(
                  text,
                  style: style,
                  overflow: TextOverflow.ellipsis,
                ),
              ),
            ],
          ),
        );
    return Container(
      color: theme.colorScheme.onSurface.withValues(alpha: 0.03),
      padding: const EdgeInsetsDirectional.fromSTEB(16, 10, 8, 10),
      child: Row(
        children: [
          const Spacer(),
          name(Icons.arrow_downward_rounded, downloadColor, l.usageDown),
          name(Icons.arrow_upward_rounded, uploadColor, l.usageUp),
          name(
            Icons.layers_outlined,
            theme.colorScheme.onSurfaceVariant,
            l.usageTotal,
            columns.total,
          ),
          const SizedBox(width: _arrowWidth),
        ],
      ),
    );
  }
}

/// A day: download and upload together; a tap shows the VPN and direct
/// parts under them.
class _DayRow extends StatefulWidget {
  const _DayRow({super.key, required this.day, required this.columns});

  final UsageDay day;
  final _Columns columns;

  @override
  State<_DayRow> createState() => _DayRowState();
}

class _DayRowState extends State<_DayRow> {
  bool _open = false;

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    final muted = theme.colorScheme.onSurfaceVariant;
    final counts = widget.day.counts;
    final all = counts.all;
    final strong = theme.textTheme.bodyMedium?.copyWith(
      fontWeight: FontWeight.w600,
    );
    final small = theme.textTheme.bodySmall?.copyWith(color: muted);
    final columns = widget.columns;
    Widget figure(int value, TextStyle? style, [double? width]) => SizedBox(
      width: width ?? columns.figure,
      child: Text(
        formatBytes(value),
        textAlign: TextAlign.end,
        style: style?.copyWith(
          fontFeatures: const [FontFeature.tabularFigures()],
        ),
        overflow: TextOverflow.ellipsis,
      ),
    );
    Widget part(IconData icon, String name, UsageBytes bytes) => Padding(
      padding: const EdgeInsets.only(top: 8),
      child: Row(
        children: [
          const SizedBox(width: 4),
          Icon(icon, size: 14, color: muted),
          const SizedBox(width: 8),
          Expanded(child: Text(name, style: small)),
          figure(bytes.down, small),
          figure(bytes.up, small),
          figure(bytes.total, small, columns.total),
          const SizedBox(width: _arrowWidth),
        ],
      ),
    );
    return InkWell(
      onTap: () => setState(() => _open = !_open),
      child: Padding(
        padding: const EdgeInsetsDirectional.fromSTEB(16, 12, 8, 12),
        child: Column(
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(_dayName(l, widget.day.date), style: strong),
                ),
                figure(all.down, theme.textTheme.bodyMedium),
                figure(all.up, theme.textTheme.bodyMedium),
                SizedBox(
                  width: columns.total,
                  child: Align(
                    alignment: AlignmentDirectional.centerEnd,
                    child: _TotalPill(bytes: all.total),
                  ),
                ),
                SizedBox(
                  width: _arrowWidth,
                  child: AnimatedRotation(
                    turns: _open ? 0.5 : 0,
                    duration: const Duration(milliseconds: 200),
                    child: Icon(
                      Icons.expand_more_rounded,
                      size: 20,
                      color: muted,
                    ),
                  ),
                ),
              ],
            ),
            AnimatedSize(
              duration: const Duration(milliseconds: 200),
              curve: Curves.easeInOut,
              alignment: Alignment.topCenter,
              child: _open
                  ? Column(
                      children: [
                        part(Icons.vpn_lock_outlined, l.usageVpn, counts.vpn),
                        part(
                          Icons.alt_route_rounded,
                          l.usageDirect,
                          counts.direct,
                        ),
                      ],
                    )
                  : const SizedBox(width: double.infinity),
            ),
          ],
        ),
      ),
    );
  }

  static String _dayName(AppLocalizations l, DateTime? date) {
    if (date == null) return '—';
    final now = DateTime.now();
    final today = DateTime(now.year, now.month, now.day);
    final day = DateTime(date.year, date.month, date.day);
    final ago = today.difference(day).inHours;
    if (ago == 0) return l.usageToday;
    // 23 to 25 hours: a day with a clock change.
    if (ago >= 23 && ago <= 25) return l.usageYesterday;
    return formatDate(l, day);
  }
}

/// A day's total, set off in a pill of the app's color.
class _TotalPill extends StatelessWidget {
  const _TotalPill({required this.bytes});

  final int bytes;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final dark = theme.brightness == Brightness.dark;
    return FittedBox(
      fit: BoxFit.scaleDown,
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
        decoration: BoxDecoration(
          color: theme.colorScheme.primary.withValues(
            alpha: dark ? 0.22 : 0.12,
          ),
          borderRadius: BorderRadius.circular(999),
        ),
        child: Text(
          formatBytes(bytes),
          maxLines: 1,
          style: theme.textTheme.bodyMedium?.copyWith(
            fontWeight: FontWeight.w700,
            fontFeatures: const [FontFeature.tabularFigures()],
          ),
        ),
      ),
    );
  }
}
